package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"lite-agent/agent"
	"lite-agent/session"
	agentpkg "lite-agent/tools/agent"

	"github.com/gorilla/websocket"
)

// ConnectionHandler 管理单个 WebSocket 连接
// 每个连接拥有独立的 Agent 实例和会话状态
type ConnectionHandler struct {
	conn     *websocket.Conn
	ag       *agent.Agent
	store    *session.Store
	sess     *session.Session
	registry *agentpkg.ToolRegistry
	provider agent.LLMProvider
	writeMu  sync.Mutex // 写锁，防止并发写入 WebSocket

	ctx    context.Context
	cancel context.CancelFunc

	// 服务引用（用于状态查询等）
	server *Server
}

// newConnectionHandler 创建连接处理器（内部使用，由 Server 调用）
func newConnectionHandler(conn *websocket.Conn, srv *Server) *ConnectionHandler {
	ctx, cancel := context.WithCancel(context.Background())

	// 为每个连接创建独立的 Agent
	ag := agent.NewAgent(srv.provider)
	ag.SetSystemPrompt(srv.systemPrompt)
	ag.SetMaxSteps(srv.maxSteps)

	// 注册工具
	for _, toolFactory := range srv.toolFactories {
		ag.AddTool(toolFactory())
	}

	// 恢复最新会话
	var sess *session.Session
	latest, err := srv.store.Latest()
	if err == nil && latest != nil {
		sess = latest
		ag.SetMemory(sess.Messages)
	} else {
		sess = session.NewSession()
	}

	h := &ConnectionHandler{
		conn:     conn,
		ag:       ag,
		store:    srv.store,
		sess:     sess,
		registry: srv.registry,
		provider: srv.provider,
		ctx:      ctx,
		cancel:   cancel,
		server:   srv,
	}

	// 设置工具调用观察者：将工具调用信息通过 WebSocket 推送
	ag.SetToolObserver(func(toolName string, args map[string]interface{}, result *agent.ToolResult) {
		if result == nil {
			// 工具调用开始（执行前通知）
			h.sendMessage(ServerMessage{
				Type:     MsgTypeToolCall,
				ToolCall: &ToolCallMsg{Name: toolName, Args: args},
			})
		} else {
			// 工具调用结束（结果通知）
			msg := ServerMessage{
				Type:   MsgTypeToolResult,
				Result: result.Content,
			}
			if result.RichData != nil {
				msg.ToolResultData = result.RichData
			}
			h.sendMessage(msg)
		}
	})

	return h
}

// Run 启动消息循环（阻塞）
func (h *ConnectionHandler) Run() {
	defer func() {
		h.saveSession()
		h.cancel()
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
			h.sendError("消息格式无效，请使用 JSON: " + err.Error())
			continue
		}

		h.handleMessage(clientMsg)
	}
}

// handleMessage 消息路由分发
func (h *ConnectionHandler) handleMessage(msg ClientMessage) {
	switch msg.Type {
	case MsgTypeChat:
		h.handleChat(msg.Content)
	case MsgTypeNewSession:
		h.handleNewSession()
	case MsgTypeLoadSession:
		h.handleLoadSession(msg.SessionID)
	case MsgTypeListSessions:
		h.handleListSessions()
	case MsgTypeDeleteSession:
		h.handleDeleteSession(msg.SessionID)
	case MsgTypeGetStatus:
		h.handleGetStatus()
	default:
		h.sendError("未知消息类型: " + msg.Type)
	}
}

// handleChat 处理对话消息
func (h *ConnectionHandler) handleChat(content string) {
	if content == "" {
		h.sendError("消息内容不能为空")
		return
	}

	// 使用独立的 context，可在连接关闭时取消
	ctx := h.ctx

	response, err := h.ag.RunStream(ctx, content,
		// onChunk: 正文流式推送
		func(chunk string) {
			h.sendMessage(ServerMessage{
				Type:    MsgTypeContent,
				Content: chunk,
			})
		},
		// onReasoning: 推理内容流式推送
		func(chunk string) {
			h.sendMessage(ServerMessage{
				Type:    MsgTypeReasoning,
				Content: chunk,
			})
		},
		// onFlush: 工具调用前的刷新（WebSocket 模式无需清屏）
		nil,
	)

	if err != nil {
		h.sendError(fmt.Sprintf("Agent 执行失败: %v", err))
		return
	}

	// 自动保存会话
	h.sess.SetMessages(h.ag.GetMemory())
	if err := h.store.Save(h.sess); err != nil {
		log.Printf("保存会话失败: %v", err)
	}

	// 发送完成消息
	info := sessionMetaToInfo(h.sess.Meta())
	h.sendMessage(ServerMessage{
		Type:     MsgTypeDone,
		Response: response,
		Session:  &info,
	})
}

// handleNewSession 创建新会话
func (h *ConnectionHandler) handleNewSession() {
	// 保存当前会话
	h.saveSession()

	// 创建新会话
	h.sess = session.NewSession()
	h.ag.SetMemory(nil)

	info := sessionMetaToInfo(h.sess.Meta())
	h.sendMessage(ServerMessage{
		Type:    MsgTypeSessionInfo,
		Session: &info,
	})
}

// handleLoadSession 加载历史会话
func (h *ConnectionHandler) handleLoadSession(sessionID string) {
	if sessionID == "" {
		h.sendError("session_id 不能为空")
		return
	}

	// 保存当前会话
	h.saveSession()

	loaded, err := h.store.Load(sessionID)
	if err != nil {
		h.sendError(fmt.Sprintf("加载会话失败: %v", err))
		return
	}

	h.sess = loaded
	h.ag.SetMemory(loaded.Messages)

	info := sessionMetaToInfo(h.sess.Meta())
	h.sendMessage(ServerMessage{
		Type:    MsgTypeSessionInfo,
		Session: &info,
	})
}

// handleListSessions 列出所有会话
func (h *ConnectionHandler) handleListSessions() {
	metas, err := h.store.List()
	if err != nil {
		h.sendError(fmt.Sprintf("读取会话列表失败: %v", err))
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
		h.sendError("session_id 不能为空")
		return
	}

	if sessionID == h.sess.ID {
		h.sendError("不能删除当前正在使用的会话")
		return
	}

	if err := h.store.Delete(sessionID); err != nil {
		h.sendError(fmt.Sprintf("删除会话失败: %v", err))
		return
	}

	h.sendMessage(ServerMessage{
		Type:   MsgTypeSessionInfo,
		Result: fmt.Sprintf("已删除会话: %s", sessionID),
	})
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

// sendConnected 发送连接建立确认
func (h *ConnectionHandler) sendConnected() {
	info := sessionMetaToInfo(h.sess.Meta())
	h.sendMessage(ServerMessage{
		Type:    MsgTypeConnected,
		Session: &info,
	})
}

// sendError 发送错误消息
func (h *ConnectionHandler) sendError(errMsg string) {
	h.sendMessage(ServerMessage{
		Type:  MsgTypeError,
		Error: errMsg,
	})
}

// sendMessage 线程安全地发送 JSON 消息到 WebSocket
func (h *ConnectionHandler) sendMessage(msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("序列化消息失败: %v", err)
		return
	}

	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	h.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := h.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("WebSocket 写入失败: %v", err)
	}
}

// saveSession 保存当前会话
func (h *ConnectionHandler) saveSession() {
	h.sess.SetMessages(h.ag.GetMemory())
	if err := h.store.Save(h.sess); err != nil {
		log.Printf("保存会话失败: %v", err)
	}
}

// heartbeat 心跳维护
func (h *ConnectionHandler) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.writeMu.Lock()
			h.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := h.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.writeMu.Unlock()
				return
			}
			h.writeMu.Unlock()
		case <-h.ctx.Done():
			return
		}
	}
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
