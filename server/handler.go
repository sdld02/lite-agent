package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"lite-agent/agent"
	"lite-agent/session"
	"lite-agent/tools"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools/task"

	"github.com/gorilla/websocket"
)

// pendingQuestion 等待用户回答的提问
type pendingQuestion struct {
	questions []tools.Question
	answerCh  chan map[string]string
	cancelCh  chan struct{} // 用于取消等待（连接断开时）
}

// SessionRunner 封装单个 session 的独立运行状态
// 每个 session 拥有独立的 Agent 实例、context 和运行标志
type SessionRunner struct {
	sess   *session.Session
	ag     *agent.Agent
	mu     sync.Mutex // 保护 ctx/cancel 的并发访问
	ctx    context.Context
	cancel context.CancelFunc

	running atomic.Bool // 是否正在执行 handleChat

	// 等待用户回答的提问（同一时间只会有一个）
	pendingQMu sync.Mutex
	pendingQ   *pendingQuestion
}

// outMsg 表示一条待发送的 WebSocket 消息
type outMsg struct {
	msgType int    // websocket.TextMessage 或 websocket.PingMessage
	data    []byte // 消息数据
}

// ConnectionHandler 管理单个 WebSocket 连接
// 支持多个 session 并发执行
type ConnectionHandler struct {
	conn     *websocket.Conn
	store    *session.Store
	registry *agentpkg.ToolRegistry
	outChan  chan outMsg // 写队列，所有 WebSocket 写入通过此 channel 序列化

	ctx    context.Context
	cancel context.CancelFunc

	// 多 session 并发支持
	runners   map[string]*SessionRunner // key = server session ID
	runnersMu sync.RWMutex
	activeSessionID string // 当前聚焦的 session ID

	// 追踪活跃的 runChat goroutine，确保关闭 outChan 前所有发送者已退出
	chatWg sync.WaitGroup

	// 服务引用（用于状态查询等）
	server *Server
}

// newConnectionHandler 创建连接处理器（内部使用，由 Server 调用）
func newConnectionHandler(conn *websocket.Conn, srv *Server) *ConnectionHandler {
	ctx, cancel := context.WithCancel(context.Background())

	h := &ConnectionHandler{
		conn:    conn,
		store:   srv.store,
		registry: srv.registry,
		outChan: make(chan outMsg, 512), // 缓冲队列，解耦消息生产与 WebSocket 写入
		ctx:     ctx,
		cancel:  cancel,
		runners: make(map[string]*SessionRunner),
		server:  srv,
	}

	// 恢复最新会话
	latest, err := srv.store.Latest()
	if err == nil && latest != nil {
		runner := h.createRunner(latest)
		h.runners[latest.ID] = runner
		h.activeSessionID = latest.ID
	} else {
		sess := session.NewSession()
		runner := h.createRunner(sess)
		h.runners[sess.ID] = runner
		h.activeSessionID = sess.ID
	}

	return h
}

// createRunner 创建一个新的 SessionRunner（初始化 Agent、注册工具、设置 memory）
func (h *ConnectionHandler) createRunner(sess *session.Session) *SessionRunner {
	ctx, cancel := context.WithCancel(h.ctx)

	ag := agent.NewAgent(h.server.GetProvider())
	ag.SetSystemPrompt(h.server.systemPrompt)
	ag.SetMaxSteps(h.server.maxSteps)

	// 注册工具
	for _, toolFactory := range h.server.toolFactories {
		tool := toolFactory()
		if tool != nil {
			ag.AddTool(tool)
		}
	}

	// 恢复 memory
	if len(sess.Messages) > 0 {
		ag.SetMemory(sess.Messages)
	}

	return &SessionRunner{
		sess:   sess,
		ag:     ag,
		ctx:    ctx,
		cancel: cancel,
	}
}

// getRunner 获取指定 session ID 的 runner（线程安全）
func (h *ConnectionHandler) getRunner(sessionID string) *SessionRunner {
	h.runnersMu.RLock()
	defer h.runnersMu.RUnlock()
	return h.runners[sessionID]
}

// getActiveRunner 获取当前聚焦 session 的 runner
func (h *ConnectionHandler) getActiveRunner() *SessionRunner {
	h.runnersMu.RLock()
	defer h.runnersMu.RUnlock()
	return h.runners[h.activeSessionID]
}

// Run 启动消息循环（阻塞）
func (h *ConnectionHandler) Run() {
	// 启动专用写入 goroutine
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.writeLoop()
	}()

	defer func() {
		h.saveAllSessions()
		h.cancelAllRunners()
		h.cancel()         // 通知所有 goroutine 停止（heartbeat 等）
		h.chatWg.Wait()    // 等待所有 runChat goroutine 退出，防止 send on closed channel
		close(h.outChan)   // 关闭写队列，writeLoop 退出
		<-writerDone       // 等待 writeLoop 完成，确保所有排队消息已写入
		h.conn.Close()
		h.server.removeConnection(h)
	}()

	// 发送连接成功消息
	h.sendConnected()

	// 设置读取超时（通过定期 ping/pong 维持）
	h.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	h.conn.SetPongHandler(func(string) error {
		h.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	// 启动心跳
	go h.heartbeat()

	// 消息读取循环
	for {
		_, rawMsg, err := h.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket 读取错误: %v", err)
			}
			return
		}

		var clientMsg ClientMessage
		if err := json.Unmarshal(rawMsg, &clientMsg); err != nil {
			h.sendError("", "消息格式无效，请使用 JSON: "+err.Error())
			continue
		}

		h.handleMessage(clientMsg)
	}
}

// handleMessage 消息路由分发
func (h *ConnectionHandler) handleMessage(msg ClientMessage) {
	switch msg.Type {
	case MsgTypeChat:
		h.handleChat(msg.SessionID, msg.Content)
	case MsgTypeNewSession:
		h.handleNewSession(msg.SessionID)
	case MsgTypeLoadSession:
		h.handleLoadSession(msg.SessionID)
	case MsgTypeListSessions:
		h.handleListSessions()
	case MsgTypeDeleteSession:
		h.handleDeleteSession(msg.SessionID)
	case MsgTypeGetTasks:
		h.handleGetTasks(msg.SessionID)
	case MsgTypeGetStatus:
		h.handleGetStatus()
	case MsgTypeCancel:
		h.handleCancel(msg.SessionID)
	case MsgTypeGetLLMConfig:
		h.handleGetLLMConfig()
	case MsgTypeSetLLMConfig:
		h.handleSetLLMConfig(msg.LLMConfig)
	case MsgTypeGetTelegramConfig:
		h.handleGetTelegramConfig()
	case MsgTypeSetTelegramConfig:
		h.handleSetTelegramConfig(msg.TelegramConfig)
	case MsgTypeStartTelegramBot:
		h.handleStartTelegramBot()
	case MsgTypeStopTelegramBot:
		h.handleStopTelegramBot()
	case MsgTypeAnswerQuestion:
		h.handleAnswerQuestion(msg.SessionID, msg.Answers)
	case MsgTypeGetMCPConfig:
		h.handleGetMCPConfig()
	case MsgTypeSetMCPConfig:
		h.handleSetMCPConfig(msg.MCPConfig)
	default:
		h.sendError("", "未知消息类型: "+msg.Type)
	}
}

// handleChat 处理对话消息（异步执行）
func (h *ConnectionHandler) handleChat(sessionID string, content string) {
	if content == "" {
		h.sendError(sessionID, "消息内容不能为空")
		return
	}

	// 确定目标 session ID
	if sessionID == "" {
		sessionID = h.activeSessionID
	}

	runner := h.getRunner(sessionID)
	if runner == nil {
		h.sendError(sessionID, "会话不存在: "+sessionID)
		return
	}

	// 原子性检查并设置 running 标志，防止同一 session 重复请求
	if !runner.running.CompareAndSwap(false, true) {
		h.sendError(sessionID, "该会话正在执行中，请等待完成或取消")
		return
	}

	// 异步执行 Agent
	h.chatWg.Add(1)
	go func() {
		defer h.chatWg.Done()
		h.runChat(runner, content)
	}()
}

// runChat 在 goroutine 中执行 Agent 对话（实际执行逻辑）
func (h *ConnectionHandler) runChat(runner *SessionRunner, content string) {
	defer runner.running.Store(false)

	sessionID := runner.sess.ID

	runner.mu.Lock()
	ctx := task.ContextWithSessionID(runner.ctx, sessionID)

	// 注入 QuestionHandler，使 ask_user_question 工具能够阻塞等待用户回答
	ctx = tools.SetQuestionHandler(ctx, func(questions []tools.Question) (map[string]string, error) {
		answerCh := make(chan map[string]string, 1)
		cancelCh := make(chan struct{}, 1)

		pq := &pendingQuestion{
			questions: questions,
			answerCh:  answerCh,
			cancelCh:  cancelCh,
		}

		// 存储在 runner 上，供 handleAnswerQuestion 使用
		runner.pendingQMu.Lock()
		runner.pendingQ = pq
		runner.pendingQMu.Unlock()

		// 确保在函数返回时清理 pendingQ
		defer func() {
			runner.pendingQMu.Lock()
			runner.pendingQ = nil
			runner.pendingQMu.Unlock()
		}()

		// 发送提问消息到前端
		h.sendSessionMessage(sessionID, ServerMessage{
			Type:      MsgTypeAskQuestion,
			Questions: questions,
		})

		// 阻塞等待用户回答或取消
		select {
		case answers := <-answerCh:
			return answers, nil
		case <-cancelCh:
			return nil, fmt.Errorf("提问已被取消")
		case <-ctx.Done():
			return nil, fmt.Errorf("会话已关闭")
		}
	})
	runner.mu.Unlock()

	response, err := runner.ag.RunStream(ctx, content, func(event agent.StreamEvent) {
		switch event.Type {
		case agent.EventContent:
			h.sendSessionMessage(sessionID, ServerMessage{Type: MsgTypeContent, Content: event.Content})
		case agent.EventReasoning:
			h.sendSessionMessage(sessionID, ServerMessage{Type: MsgTypeReasoning, Content: event.Content})
		case agent.EventToolCallProgress:
			h.sendSessionMessage(sessionID, ServerMessage{
				Type:             MsgTypeToolCallProgress,
				ToolCallProgress: &ToolCallProgressMsg{Name: event.ToolName, ArgsBytes: event.ArgsBytes},
			})
		case agent.EventToolCallStart:
			h.sendSessionMessage(sessionID, ServerMessage{
				Type:     MsgTypeToolCall,
				ToolCall: &ToolCallMsg{Name: event.ToolName, Args: event.ToolArgs},
			})
		case agent.EventToolCallEnd:
			msg := ServerMessage{Type: MsgTypeToolResult, Result: event.ToolResult.Content}
			if event.ToolResult.RichData != nil {
				msg.ToolResultData = event.ToolResult.RichData
			}
			h.sendSessionMessage(sessionID, msg)
			// 任务管理工具执行后，主动推送最新任务列表
			if event.ToolName == "task_create" || event.ToolName == "task_update" || event.ToolName == "task_list" {
				h.pushTasks(sessionID)
			}
		case agent.EventFlush:
			// WebSocket 模式无需清屏
		}
	})

	if err != nil {
		h.sendSessionMessage(sessionID, ServerMessage{
			Type:  MsgTypeError,
			Error: fmt.Sprintf("Agent 执行失败: %v", err),
		})
		return
	}

	// 自动保存会话
	runner.sess.SetMessages(runner.ag.GetMemory())
	if err := h.store.Save(runner.sess); err != nil {
		log.Printf("保存会话失败: %v", err)
	}

	// 发送完成消息
	info := sessionMetaToInfo(runner.sess.Meta())
	h.sendSessionMessage(sessionID, ServerMessage{
		Type:     MsgTypeDone,
		Response: response,
		Session:  &info,
	})
}

// handleNewSession 创建新会话
func (h *ConnectionHandler) handleNewSession(clientSessionID string) {
	// 保存当前聚焦 session
	if runner := h.getActiveRunner(); runner != nil {
		h.saveRunnerSession(runner)
	}

	// 创建新会话和 runner
	sess := session.NewSession()
	runner := h.createRunner(sess)

	h.runnersMu.Lock()
	h.runners[sess.ID] = runner
	h.activeSessionID = sess.ID
	h.runnersMu.Unlock()

	log.Printf("[handleNewSession] server_id=%s, client_id=%s", sess.ID, clientSessionID)

	info := sessionMetaToInfo(sess.Meta())
	h.sendSessionMessage(sess.ID, ServerMessage{
		Type:            MsgTypeSessionInfo,
		Session:         &info,
		ClientSessionID: clientSessionID, // 回传客户端 session ID，用于精确关联
	})
}

// handleLoadSession 加载历史会话
func (h *ConnectionHandler) handleLoadSession(sessionID string) {
	if sessionID == "" {
		h.sendError("", "session_id 不能为空")
		return
	}

	// 保存当前聚焦 session
	if runner := h.getActiveRunner(); runner != nil {
		h.saveRunnerSession(runner)
	}

	// 检查是否已有 runner
	existing := h.getRunner(sessionID)
	if existing != nil {
		// 已有 runner，直接切换焦点
		h.runnersMu.Lock()
		h.activeSessionID = sessionID
		h.runnersMu.Unlock()
		h.sendSessionLoaded(existing)
		return
	}

	// 从 store 加载
	loaded, err := h.store.Load(sessionID)
	if err != nil {
		h.sendError(sessionID, fmt.Sprintf("加载会话失败: %v", err))
		return
	}

	runner := h.createRunner(loaded)
	h.runnersMu.Lock()
	h.runners[sessionID] = runner
	h.activeSessionID = sessionID
	h.runnersMu.Unlock()

	h.sendSessionLoaded(runner)
}

// handleListSessions 列出所有会话
func (h *ConnectionHandler) handleListSessions() {
	metas, err := h.store.List()
	if err != nil {
		h.sendError("", fmt.Sprintf("读取会话列表失败: %v", err))
		return
	}

	sessions := make([]SessionInfo, 0, len(metas))
	for _, m := range metas {
		sessions = append(sessions, sessionMetaToInfo(m))
	}

	h.sendMessage(ServerMessage{
		Type:     MsgTypeSessionList,
		Sessions: sessions,
	})
}

// handleDeleteSession 删除会话
func (h *ConnectionHandler) handleDeleteSession(sessionID string) {
	if sessionID == "" {
		h.sendError("", "session_id 不能为空")
		return
	}

	if sessionID == h.activeSessionID {
		h.sendError(sessionID, "不能删除当前正在使用的会话")
		return
	}

	// 如果有对应 runner，先取消并移除
	h.runnersMu.Lock()
	if runner, ok := h.runners[sessionID]; ok {
		runner.cancel()
		delete(h.runners, sessionID)
	}
	h.runnersMu.Unlock()

	if err := h.store.Delete(sessionID); err != nil {
		h.sendError(sessionID, fmt.Sprintf("删除会话失败: %v", err))
		return
	}

	h.sendSessionMessage(sessionID, ServerMessage{
		Type:   MsgTypeSessionInfo,
		Result: fmt.Sprintf("已删除会话: %s", sessionID),
	})
}

// handleGetTasks 返回指定会话的任务列表
func (h *ConnectionHandler) handleGetTasks(sessionID string) {
	if sessionID == "" {
		sessionID = h.activeSessionID
	}

	taskMgr := h.server.taskMgr
	if taskMgr == nil || taskMgr.Store == nil {
		h.sendSessionMessage(sessionID, ServerMessage{
			Type:  MsgTypeTasks,
			Tasks: []TaskInfo{},
		})
		return
	}

	tasks, err := taskMgr.Store.List(sessionID)
	if err != nil {
		h.sendError(sessionID, fmt.Sprintf("读取任务列表失败: %v", err))
		return
	}

	filtered := filterTasks(tasks)
	h.sendSessionMessage(sessionID, ServerMessage{
		Type:  MsgTypeTasks,
		Tasks: filtered,
	})
}

// handleCancel 取消指定 session 的执行
func (h *ConnectionHandler) handleCancel(sessionID string) {
	if sessionID == "" {
		sessionID = h.activeSessionID
	}

	runner := h.getRunner(sessionID)
	if runner == nil {
		h.sendError(sessionID, "会话不存在: "+sessionID)
		return
	}

	if !runner.running.Load() {
		h.sendError(sessionID, "该会话当前没有在执行任务")
		return
	}

	// 取消当前 context，Agent.RunStream 会通过 ctx 感知取消
	runner.mu.Lock()
	runner.cancel()

	// 取消等待中的提问（如果有的话）
	runner.pendingQMu.Lock()
	if runner.pendingQ != nil {
		close(runner.pendingQ.cancelCh)
		runner.pendingQ = nil
	}
	runner.pendingQMu.Unlock()

	// 重建 runner 的 context（以便后续继续使用该 session）
	newCtx, newCancel := context.WithCancel(h.ctx)
	runner.ctx = newCtx
	runner.cancel = newCancel
	runner.mu.Unlock()
}

// handleGetStatus 返回服务状态
func (h *ConnectionHandler) handleGetStatus() {
	uptime := time.Since(h.server.startTime).Round(time.Second).String()
	h.sendMessage(ServerMessage{
		Type: MsgTypeStatus,
		Status: &StatusInfo{
			ActiveConnections: h.server.activeConnectionCount(),
			Uptime:            uptime,
			Version:           "0.1.0",
		},
	})
}

// pushTasks 主动推送指定会话的任务列表
func (h *ConnectionHandler) pushTasks(sessionID string) {
	taskMgr := h.server.taskMgr
	if taskMgr == nil || taskMgr.Store == nil {
		return
	}

	tasks, err := taskMgr.Store.List(sessionID)
	if err != nil {
		return
	}

	filtered := filterTasks(tasks)
	h.sendSessionMessage(sessionID, ServerMessage{
		Type:  MsgTypeTasks,
		Tasks: filtered,
	})
}

// sendConnected 发送连接建立确认
func (h *ConnectionHandler) sendConnected() {
	runner := h.getActiveRunner()
	if runner == nil {
		h.sendMessage(ServerMessage{Type: MsgTypeConnected})
		return
	}
	info := sessionMetaToInfo(runner.sess.Meta())
	h.sendSessionMessage(runner.sess.ID, ServerMessage{
		Type:    MsgTypeConnected,
		Session: &info,
	})
}

// sendSessionLoaded 发送会话加载完成消息（含完整历史消息）
func (h *ConnectionHandler) sendSessionLoaded(runner *SessionRunner) {
	info := sessionMetaToInfo(runner.sess.Meta())
	sessionID := runner.sess.ID

	// 序列化历史消息
	messages := make([]json.RawMessage, 0, len(runner.sess.Messages))
	for _, msg := range runner.sess.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			log.Printf("序列化消息失败: %v", err)
			continue
		}
		messages = append(messages, data)
	}

	h.sendSessionMessage(sessionID, ServerMessage{
		Type:     MsgTypeSessionLoaded,
		Session:  &info,
		Messages: messages,
	})
}

// sendError 发送错误消息（带 session_id）
func (h *ConnectionHandler) sendError(sessionID string, errMsg string) {
	h.sendSessionMessage(sessionID, ServerMessage{
		Type:  MsgTypeError,
		Error: errMsg,
	})
}

// sendSessionMessage 发送携带 session_id 的消息
func (h *ConnectionHandler) sendSessionMessage(sessionID string, msg ServerMessage) {
	msg.SessionID = sessionID
	h.sendMessage(msg)
}

// sendMessage 将消息推入写队列（非阻塞，不直接写 WebSocket）
// 多个 goroutine 可并发调用而不会相互阻塞
func (h *ConnectionHandler) sendMessage(msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("序列化消息失败: %v", err)
		return
	}

	select {
	case h.outChan <- outMsg{msgType: websocket.TextMessage, data: data}:
	case <-h.ctx.Done():
		// 连接正在关闭，丢弃消息
	}
}

// writeLoop 专用写入 goroutine：从 channel 读取消息并写入 WebSocket
// 所有 WebSocket 写操作都通过此 goroutine 序列化，无需互斥锁
func (h *ConnectionHandler) writeLoop() {
	for msg := range h.outChan {
		h.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := h.conn.WriteMessage(msg.msgType, msg.data); err != nil {
			log.Printf("WebSocket 写入失败: %v", err)
			return
		}
	}
}

// saveRunnerSession 保存指定 runner 的会话
func (h *ConnectionHandler) saveRunnerSession(runner *SessionRunner) {
	runner.sess.SetMessages(runner.ag.GetMemory())
	if err := h.store.Save(runner.sess); err != nil {
		log.Printf("保存会话失败: %v", err)
	}
}

// saveAllSessions 保存所有 runner 的会话
func (h *ConnectionHandler) saveAllSessions() {
	h.runnersMu.RLock()
	defer h.runnersMu.RUnlock()
	for _, runner := range h.runners {
		runner.sess.SetMessages(runner.ag.GetMemory())
		if err := h.store.Save(runner.sess); err != nil {
			log.Printf("保存会话失败: %v", err)
		}
	}
}

// cancelAllRunners 取消所有 runner 的 context
func (h *ConnectionHandler) cancelAllRunners() {
	h.runnersMu.RLock()
	defer h.runnersMu.RUnlock()
	for _, runner := range h.runners {
		runner.mu.Lock()
		runner.cancel()
		runner.mu.Unlock()
	}
}

// heartbeat 心跳维护（通过写队列发送 ping）
func (h *ConnectionHandler) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			select {
			case h.outChan <- outMsg{msgType: websocket.PingMessage, data: nil}:
			case <-h.ctx.Done():
				return
			}
		case <-h.ctx.Done():
			return
		}
	}
}

// filterTasks 过滤 _internal metadata 的任务并转换格式
func filterTasks(tasks []task.Task) []TaskInfo {
	var filtered []TaskInfo
	for _, t := range tasks {
		if t.Metadata != nil {
			if _, ok := t.Metadata["_internal"]; ok {
				continue
			}
		}
		filtered = append(filtered, TaskInfo{
			ID:        t.ID,
			Subject:   t.Subject,
			Status:    string(t.Status),
			Owner:     t.Owner,
			BlockedBy: t.BlockedBy,
		})
	}
	if filtered == nil {
		filtered = []TaskInfo{}
	}
	return filtered
}

// sessionMetaToInfo 将 session.Meta 转换为 SessionInfo
func sessionMetaToInfo(m session.Meta) SessionInfo {
	return SessionInfo{
		ID:           m.ID,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		MessageCount: m.MessageCount,
		Preview:      m.Preview,
	}
}

// handleGetLLMConfig 处理获取 LLM 配置请求
func (h *ConnectionHandler) handleGetLLMConfig() {
	cfg := h.server.GetLLMConfig()
	h.sendMessage(ServerMessage{
		Type:      MsgTypeLLMConfig,
		LLMConfig: &cfg,
	})
}

// handleSetLLMConfig 处理设置 LLM 配置请求
func (h *ConnectionHandler) handleSetLLMConfig(input *LLMConfigInfo) {
	if input == nil {
		h.sendError("", "LLM 配置不能为空")
		return
	}

	cfg := h.server.SetLLMConfig(*input)
	h.sendMessage(ServerMessage{
		Type:      MsgTypeLLMConfig,
		LLMConfig: &cfg,
	})
}

// handleGetTelegramConfig 处理获取 Telegram Bot 配置请求
func (h *ConnectionHandler) handleGetTelegramConfig() {
	cfg := h.server.GetTelegramConfig()
	h.sendMessage(ServerMessage{
		Type:           MsgTypeTelegramConfig,
		TelegramConfig: &cfg,
	})
}

// handleGetMCPConfig 处理获取 MCP 服务器配置请求
func (h *ConnectionHandler) handleGetMCPConfig() {
	cfg := h.server.GetMCPConfig()
	h.sendMessage(ServerMessage{
		Type:      MsgTypeMCPConfig,
		MCPConfig: &cfg,
	})
}

// handleSetMCPConfig 处理保存 MCP 服务器配置请求
func (h *ConnectionHandler) handleSetMCPConfig(input *MCPConfigInfo) {
	if input == nil {
		h.sendError("", "MCP 配置不能为空")
		return
	}

	cfg, err := h.server.SetMCPConfig(*input)
	if err != nil {
		h.sendError("", "保存 MCP 配置失败: "+err.Error())
		return
	}
	h.sendMessage(ServerMessage{
		Type:      MsgTypeMCPConfig,
		MCPConfig: &cfg,
	})
}

// handleAnswerQuestion 处理用户回答提问
func (h *ConnectionHandler) handleAnswerQuestion(sessionID string, answers map[string]string) {
	if sessionID == "" {
		sessionID = h.activeSessionID
	}

	if answers == nil || len(answers) == 0 {
		h.sendError(sessionID, "答案不能为空")
		return
	}

	runner := h.getRunner(sessionID)
	if runner == nil {
		h.sendError(sessionID, "会话不存在: "+sessionID)
		return
	}

	runner.pendingQMu.Lock()
	pq := runner.pendingQ
	runner.pendingQMu.Unlock()

	if pq == nil {
		h.sendError(sessionID, "当前没有等待回答的提问")
		return
	}

	// 将答案发送到等待通道（非阻塞发送，防止重复回答导致阻塞）
	select {
	case pq.answerCh <- answers:
		// 答案已发送
	default:
		h.sendError(sessionID, "已收到过该提问的答案")
	}
}

// handleSetTelegramConfig 处理设置 Telegram Bot Token
func (h *ConnectionHandler) handleSetTelegramConfig(input *TelegramConfigInfo) {
	if input == nil || input.Token == "" {
		h.sendError("", "Telegram Bot Token 不能为空")
		return
	}

	// 如果 Token 包含脱敏标记，保留旧 Token
	if !containsMasked(input.Token) {
		h.server.SetTelegramConfig(input.Token)
	}

	// 返回当前配置
	cfg := h.server.GetTelegramConfig()
	h.sendMessage(ServerMessage{
		Type:           MsgTypeTelegramConfig,
		TelegramConfig: &cfg,
	})
}

// handleStartTelegramBot 处理启动 Telegram Bot
func (h *ConnectionHandler) handleStartTelegramBot() {
	if err := h.server.StartTelegramBot(); err != nil {
		h.sendError("", err.Error())
		return
	}

	cfg := h.server.GetTelegramConfig()
	h.sendMessage(ServerMessage{
		Type:           MsgTypeTelegramConfig,
		TelegramConfig: &cfg,
	})
}

// handleStopTelegramBot 处理停止 Telegram Bot
func (h *ConnectionHandler) handleStopTelegramBot() {
	h.server.StopTelegramBot()

	cfg := h.server.GetTelegramConfig()
	h.sendMessage(ServerMessage{
		Type:           MsgTypeTelegramConfig,
		TelegramConfig: &cfg,
	})
}
