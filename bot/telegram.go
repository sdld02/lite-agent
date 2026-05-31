package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"lite-agent/agent"
	"lite-agent/session"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ToolFactory 工具工厂函数类型（与 server 包保持一致）
type ToolFactory func() agent.Tool

// Config Telegram Bot 配置
type Config struct {
	Token        string               // Bot Token
	SystemPrompt string               // 系统提示词
	MaxSteps     int                  // 最大执行步数
	Registry     *agentpkg.ToolRegistry // 工具注册表（用于子 Agent 工具）
	ProviderCfg  agent.LLMProvider    // LLM Provider
}

// Bot Telegram Bot 结构体
type Bot struct {
	cfg      Config
	api      *tgbotapi.BotAPI
	store    *session.Store
	registry *agentpkg.ToolRegistry

	// 每个 chat 一个 session runner
	runners   map[int64]*chatRunner
	runnersMu sync.RWMutex
}

// chatRunner 单个聊天会话的运行状态
type chatRunner struct {
	sess    *session.Session
	ag      *agent.Agent
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	running bool // 是否正在执行
}

// toolInfo 工具调用信息（用于流式显示）
type toolInfo struct {
	name string
	args map[string]interface{}
}

// New 创建 Telegram Bot 实例
func New(cfg Config) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("创建 Telegram Bot API 失败: %w", err)
	}

	api.Debug = false // 设为 true 可查看详细日志

	log.Printf("🤖 Telegram Bot 已授权，用户名: @%s", api.Self.UserName)

	// 初始化会话存储
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	store, err := session.NewStore(filepath.Join(homeDir, ".lite-agent", "telegram_sessions"))
	if err != nil {
		return nil, fmt.Errorf("初始化会话存储失败: %w", err)
	}

	return &Bot{
		cfg:      cfg,
		api:      api,
		store:    store,
		registry: cfg.Registry,
		runners:  make(map[int64]*chatRunner),
	}, nil
}

// Start 启动 Bot（长轮询模式，阻塞），自动注册 SIGINT/SIGTERM 信号处理。
// 如果 Bot 运行在已有信号管理的进程中（如 server 模式），请使用 StartWithoutSignal。
func (b *Bot) Start() error {
	return b.start(true)
}

// StartWithoutSignal 启动 Bot（长轮询模式，阻塞），不注册信号处理。
// 适用于上层（如 server）已统一管理信号处理的场景。
func (b *Bot) StartWithoutSignal() error {
	return b.start(false)
}

func (b *Bot) start(withSignalHandler bool) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	log.Println("🚀 Telegram Bot 已启动，等待消息...")

	if withSignalHandler {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		for {
			select {
			case update := <-updates:
				if update.Message == nil {
					continue
				}
				go b.handleMessage(update.Message)

			case <-sigChan:
				log.Println("🛑 收到退出信号，正在关闭 Telegram Bot...")
				b.saveAllSessions()
				b.api.StopReceivingUpdates()
				return nil
			}
		}
	}

	// 无信号处理模式
	for update := range updates {
		if update.Message == nil {
			continue
		}
		go b.handleMessage(update.Message)
	}

	return nil
}

// handleMessage 处理接收到的消息
func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	userName := msg.Chat.UserName
	if userName == "" {
		userName = msg.Chat.FirstName
	}
	text := msg.Text

	if text == "" {
		return
	}

	log.Printf("📩 [%s] (chat=%d): %s", userName, chatID, text)

	// 处理命令
	if strings.HasPrefix(text, "/") {
		b.handleCommand(chatID, text)
		return
	}

	// 正常对话
	b.handleChat(chatID, text)
}

// handleCommand 处理 Telegram 命令
func (b *Bot) handleCommand(chatID int64, text string) {
	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/start":
		b.sendText(chatID, "👋 你好！我是 AI Agent Bot。\n\n"+
			"你可以直接向我提问或让我执行任务。\n\n"+
			"支持的命令：\n"+
			"/new - 开始新会话\n"+
			"/sessions - 查看历史会话\n"+
			"/load <id> - 加载历史会话\n"+
			"/cancel - 取消当前执行\n"+
			"/status - 查看状态")

	case "/new":
		b.createNewSession(chatID)

	case "/sessions":
		b.listSessions(chatID)

	case "/load":
		if len(parts) < 2 {
			b.sendText(chatID, "用法: /load <session-id>")
			return
		}
		b.loadSession(chatID, parts[1])

	case "/cancel":
		b.cancelChat(chatID)

	case "/status":
		b.showStatus(chatID)

	default:
		b.sendText(chatID, "未知命令。支持: /new, /sessions, /load, /cancel, /status")
	}
}

// handleChat 处理普通对话
func (b *Bot) handleChat(chatID int64, text string) {
	runner := b.getOrCreateRunner(chatID)
	if runner == nil {
		b.sendText(chatID, "❌ 初始化会话失败")
		return
	}

	// 检查是否正在执行
	runner.mu.Lock()
	if runner.running {
		runner.mu.Unlock()
		b.sendText(chatID, "⏳ 当前会话正在执行中，请等待完成或使用 /cancel 取消")
		return
	}
	runner.running = true

	// 每次执行使用新的 context
	runner.ctx, runner.cancel = context.WithCancel(context.Background())
	ctx := runner.ctx
	runner.mu.Unlock()

	defer func() {
		runner.mu.Lock()
		runner.running = false
		runner.mu.Unlock()
	}()

	// 发送"正在思考"消息（稍后会被编辑为实际响应）
	// Bug 修复 #2: 检查发送结果，避免 nil
	thinkingMsg, err := b.api.Send(tgbotapi.NewMessage(chatID, "🤔 思考中..."))
	if err != nil || thinkingMsg.MessageID == 0 {
		log.Printf("发送「思考中」消息失败 (chat=%d): %v", chatID, err)
		b.sendText(chatID, "❌ 服务暂时不可用，请稍后重试")
		return
	}

	// 累积内容
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var lastEditTime time.Time
	const editInterval = 800 * time.Millisecond // 编辑间隔

	// 工具调用后需要创建新的"思考中"消息
	var needNewBubble bool

	// 工具调用信息
	var pendingToolCalls []toolInfo

	// 启动新的 thinking bubble：发送新消息并重置累积器
	startNewBubble := func() {
		newMsg, err := b.api.Send(tgbotapi.NewMessage(chatID, "🤔 ..."))
		if err != nil || newMsg.MessageID == 0 {
			log.Printf("创建新气泡失败 (chat=%d): %v", chatID, err)
			return
		}
		thinkingMsg = newMsg
		contentBuilder.Reset()
		reasoningBuilder.Reset()
		lastEditTime = time.Time{} // 重置限频计时，让新消息立即显示
		needNewBubble = false
	}

	// editOrThrottle 限频编辑消息
	editOrThrottle := func(final bool) {
		now := time.Now()
		if !final && now.Sub(lastEditTime) < editInterval {
			return
		}
		lastEditTime = now

		rawText := buildDisplayText(contentBuilder.String(), reasoningBuilder.String(), pendingToolCalls)
		if rawText == "" {
			return
		}

		// Bug 修复 #3: 按 rune 截断，避免破坏 UTF-8 多字节字符
		text := truncateByRunes(rawText, 4000)
		if len(text) < len(rawText) {
			text += "\n\n...（内容过长已截断）"
		}

		edit := tgbotapi.NewEditMessageText(chatID, thinkingMsg.MessageID, text)
		edit.ParseMode = tgbotapi.ModeMarkdownV2
		if _, err := b.api.Send(edit); err != nil {
			// 编辑失败（可能内容未变或消息太旧），忽略
			if !strings.Contains(err.Error(), "message is not modified") &&
				!strings.Contains(err.Error(), "message to edit not found") {
				log.Printf("编辑消息失败 (chat=%d): %v", chatID, err)
			}
		}
	}

	// 运行 Agent（流式）
	response, err := runner.ag.RunStream(ctx, text, func(event agent.StreamEvent) {
		switch event.Type {
		case agent.EventContent:
			if needNewBubble {
				startNewBubble()
			}
			contentBuilder.WriteString(event.Content)
			editOrThrottle(false)

		case agent.EventReasoning:
			if needNewBubble {
				startNewBubble()
			}
			reasoningBuilder.WriteString(event.Content)
			editOrThrottle(false)

		case agent.EventToolCallStart:
			// 先强制刷新流式内容到「思考中」消息，确保在工具消息之前到达
			editOrThrottle(true)

			pendingToolCalls = append(pendingToolCalls, toolInfo{
				name: event.ToolName,
				args: event.ToolArgs,
			})
			// 工具调用开始，发送单独消息
			toolName := escapeMarkdownV2(event.ToolName)
			toolText := fmt.Sprintf("🔧 调用工具: *%s*", toolName)
			if event.ToolName == "shell" {
				if intent, ok := event.ToolArgs["intent"]; ok {
					toolText += fmt.Sprintf("\n   意图: %s", escapeMarkdownV2(fmt.Sprintf("%v", intent)))
				}
				if cmd, ok := event.ToolArgs["command"]; ok {
					cmdStr := fmt.Sprintf("%v", cmd)
					toolText += fmt.Sprintf("\n   命令: `%s`", escapeMarkdownV2(cmdStr))
				}
			}
			b.sendMarkdown(chatID, toolText)

		case agent.EventToolCallEnd:
			// Bug 修复 #6: 从 pending 中正确移除（倒序遍历，匹配最后一个同名工具）
			for i := len(pendingToolCalls) - 1; i >= 0; i-- {
				if pendingToolCalls[i].name == event.ToolName {
					pendingToolCalls = append(pendingToolCalls[:i], pendingToolCalls[i+1:]...)
					break
				}
			}
			if event.ToolResult != nil {
				if event.ToolResult.IsError {
					b.sendText(chatID, fmt.Sprintf("❌ 错误: %s", event.ToolResult.Content))
				} else {
					// 工具结果太长则截断
					result := event.ToolResult.Content
					if utf8.RuneCountInString(result) > 500 {
						result = truncateByRunes(result, 500) + "..."
					}
					b.sendText(chatID, fmt.Sprintf("✅ 完成: %s", result))
				}
			}
			// 工具调用后，下一轮 LLM 输出应该从新消息开始（而非继续编辑旧消息）
			needNewBubble = true

		case agent.EventFlush:
			// 刷新当前内容
			editOrThrottle(true)
		}
	})

	if err != nil {
		if ctx.Err() == context.Canceled {
			b.sendText(chatID, "⏹ 已取消")
		} else {
			b.sendText(chatID, fmt.Sprintf("❌ Agent 执行失败: %v", err))
		}
		// 删除"思考中"消息
		b.api.Send(tgbotapi.NewDeleteMessage(chatID, thinkingMsg.MessageID))
		return
	}

	// Bug 修复 #4: 使用流式累积的内容构建最终响应（保留推理过程）
	finalText := buildDisplayText(contentBuilder.String(), reasoningBuilder.String(), nil)
	// 如果流式过程中没有任何内容积累，回退到 RunStream 的返回值
	if finalText == "" {
		finalText = escapeMarkdownV2(response)
	}
	if finalText == "" {
		finalText = "（无内容）"
	}

	// 长消息分段发送
	if utf8.RuneCountInString(finalText) > 4000 {
		// 删除"思考中"消息
		b.api.Send(tgbotapi.NewDeleteMessage(chatID, thinkingMsg.MessageID))
		// 分段发送
		for _, chunk := range splitMessage(finalText, 4000) {
			b.sendMarkdown(chatID, chunk)
		}
	} else {
		edit := tgbotapi.NewEditMessageText(chatID, thinkingMsg.MessageID, finalText)
		edit.ParseMode = tgbotapi.ModeMarkdownV2
		if _, err := b.api.Send(edit); err != nil {
			// 编辑失败（可能消息已被删除），直接发送
			b.sendMarkdown(chatID, finalText)
		}
	}

	// 保存会话
	b.saveRunnerSession(chatID)
}

// getOrCreateRunner 获取或创建指定 chat 的 runner
func (b *Bot) getOrCreateRunner(chatID int64) *chatRunner {
	b.runnersMu.RLock()
	runner, exists := b.runners[chatID]
	b.runnersMu.RUnlock()

	if exists {
		// 重建 context（每次对话使用新 context）
		runner.mu.Lock()
		runner.ctx, runner.cancel = context.WithCancel(context.Background())
		runner.mu.Unlock()
		return runner
	}

	// 创建新会话
	return b.createRunner(chatID, session.NewSession())
}

// createNewSession 创建新会话
func (b *Bot) createNewSession(chatID int64) {
	// 保存旧会话
	b.saveRunnerSession(chatID)

	// 创建新会话
	b.createRunner(chatID, session.NewSession())
	b.sendText(chatID, "✨ 已创建新会话")
}

// createRunner 创建 runner 并注册
func (b *Bot) createRunner(chatID int64, sess *session.Session) *chatRunner {
	ctx, cancel := context.WithCancel(context.Background())

	ag := agent.NewAgent(b.cfg.ProviderCfg)
	ag.SetSystemPrompt(b.cfg.SystemPrompt)
	ag.SetMaxSteps(b.cfg.MaxSteps)

	// 注册工具（与 main.go 保持一致）
	ag.AddTool(tools.NewCalculatorTool())
	ag.AddTool(tools.NewSystemInfoTool())
	ag.AddTool(tools.NewShellToolUnsafe())
	ag.AddTool(tools.NewFileEditTool())
	ag.AddTool(tools.NewFileWriteTool())
	ag.AddTool(tools.NewFileDiffTool())
	ag.AddTool(tools.NewFileReadTool())
	ag.AddTool(tools.NewCodeProbeTool())
	ag.AddTool(tools.NewCodeStatsTool())
	ag.AddTool(tools.NewLSPTool())
	ag.AddTool(tools.NewTaskCreateTool())
	ag.AddTool(tools.NewTaskUpdateTool())
	ag.AddTool(tools.NewTaskListTool())
	ag.AddTool(tools.NewTaskGetTool())
	// AskUserQuestion 用户提问工具
	ag.AddTool(tools.NewAskUserQuestionTool())
	// Grep 代码搜索工具
	ag.AddTool(tools.NewGrepTool())
	// Glob 文件名匹配工具
	ag.AddTool(tools.NewGlobTool())
	// WebFetch 网页抓取工具
	ag.AddTool(tools.NewWebFetchTool(b.cfg.ProviderCfg))
	// WebSearch 网页搜索工具
	ag.AddTool(tools.NewWebSearchTool())
	// 子 Agent 工具（使用传入的 registry）
	if b.registry != nil {
		ag.AddTool(tools.NewAgentTool(b.registry, b.cfg.ProviderCfg))
	}
	// Skill 技能工具
	homeDir, _ := os.UserHomeDir()
	workDir, _ := os.Getwd()
	ag.AddTool(tools.NewSkillTool(homeDir, workDir, b.registry, b.cfg.ProviderCfg))
	// MCP 工具
	if mgr := tools.GetMCPManager(); mgr != nil && mgr.HasServers() {
		ag.AddTool(tools.NewMCPTool(mgr))
	}

	// 恢复 memory
	if len(sess.Messages) > 0 {
		ag.SetMemory(sess.Messages)
	}

	runner := &chatRunner{
		sess:   sess,
		ag:     ag,
		ctx:    ctx,
		cancel: cancel,
	}

	b.runnersMu.Lock()
	b.runners[chatID] = runner
	b.runnersMu.Unlock()

	return runner
}

// listSessions 列出历史会话
func (b *Bot) listSessions(chatID int64) {
	metas, err := b.store.List()
	if err != nil {
		b.sendText(chatID, fmt.Sprintf("❌ 读取会话列表失败: %v", err))
		return
	}

	if len(metas) == 0 {
		b.sendText(chatID, "暂无历史会话")
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 *历史会话：*\n\n")

	// 找到当前 chat 的活跃 session ID
	b.runnersMu.RLock()
	currentRunner, hasCurrent := b.runners[chatID]
	b.runnersMu.RUnlock()
	var currentID string
	if hasCurrent {
		currentID = currentRunner.sess.ID
	}

	for i, m := range metas {
		if i >= 20 {
			sb.WriteString("...（仅显示最近 20 条）\n")
			break
		}
		marker := "  "
		if m.ID == currentID {
			marker = "🟢 "
		}
		displayTime := m.UpdatedAt
		if len(displayTime) >= 16 {
			displayTime = displayTime[:16]
		}
		sb.WriteString(fmt.Sprintf("%s`%s`\n", marker, m.ID[:8]))
		sb.WriteString(fmt.Sprintf("   %s \\| %d 条消息\n", displayTime, m.MessageCount))
		if m.Preview != "" {
			preview := m.Preview
			if utf8.RuneCountInString(preview) > 60 {
				preview = truncateByRunes(preview, 60) + "..."
			}
			sb.WriteString(fmt.Sprintf("   _%s_\n", escapeMarkdownV2(preview)))
		}
		sb.WriteString("\n")
	}

	b.sendMarkdown(chatID, sb.String())
}

// loadSession 加载历史会话
func (b *Bot) loadSession(chatID int64, sessionID string) {
	// 保存当前会话
	b.saveRunnerSession(chatID)

	loaded, err := b.store.Load(sessionID)
	if err != nil {
		// 尝试模糊匹配（前缀匹配）
		found := false
		metas, listErr := b.store.List()
		if listErr == nil {
			for _, m := range metas {
				if strings.HasPrefix(m.ID, sessionID) {
					loaded, err = b.store.Load(m.ID)
					if err == nil {
						found = true
						break
					}
				}
			}
		}
		if !found {
			b.sendText(chatID, fmt.Sprintf("❌ 加载会话失败: %v", err))
			return
		}
	}

	b.createRunner(chatID, loaded)
	b.sendText(chatID, fmt.Sprintf("📂 已加载会话 `%s`（%d 条消息）", loaded.ID[:8], loaded.MessageCount))
}

// cancelChat 取消当前执行
func (b *Bot) cancelChat(chatID int64) {
	b.runnersMu.RLock()
	runner, exists := b.runners[chatID]
	b.runnersMu.RUnlock()

	if !exists || !runner.running {
		b.sendText(chatID, "当前没有正在执行的任务")
		return
	}

	runner.mu.Lock()
	if runner.cancel != nil {
		runner.cancel()
	}
	runner.mu.Unlock()

	b.sendText(chatID, "⏹ 已发送取消信号...")
}

// showStatus 显示状态
func (b *Bot) showStatus(chatID int64) {
	b.runnersMu.RLock()
	runner, exists := b.runners[chatID]
	b.runnersMu.RUnlock()

	var status string
	if !exists {
		status = "未初始化"
	} else {
		status = fmt.Sprintf(
			"会话ID: `%s`\n消息数: %d\n状态: %s",
			runner.sess.ID[:8],
			runner.sess.MessageCount,
			map[bool]string{true: "🟢 执行中", false: "⏸ 空闲"}[runner.running],
		)
	}

	b.sendMarkdown(chatID, "📊 *状态*\n\n"+status)
}

// sendText 发送纯文本消息
func (b *Bot) sendText(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("发送消息失败 (chat=%d): %v", chatID, err)
	}
}

// sendMarkdown 发送 MarkdownV2 格式消息，自动处理分段和转义回退
func (b *Bot) sendMarkdown(chatID int64, text string) {
	if utf8.RuneCountInString(text) > 4000 {
		// 分段发送
		for i, chunk := range splitMessage(text, 4000) {
			if i > 0 {
				// 分段间短暂延迟，避免被 Telegram 限频
				time.Sleep(200 * time.Millisecond)
			}
			b.sendSingleMarkdown(chatID, chunk)
		}
		return
	}
	b.sendSingleMarkdown(chatID, text)
}

// sendSingleMarkdown 发送单条 MarkdownV2 消息（不超过 4000 字符）
func (b *Bot) sendSingleMarkdown(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("MarkdownV2 发送失败 (chat=%d, len=%d): %v", chatID, len(text), err)
		// 回退到纯文本
		msg.ParseMode = ""
		if _, err2 := b.api.Send(msg); err2 != nil {
			log.Printf("纯文本回退也失败 (chat=%d): %v", chatID, err2)
		}
	}
}

// saveRunnerSession 保存指定 chat 的会话
func (b *Bot) saveRunnerSession(chatID int64) {
	b.runnersMu.RLock()
	runner, exists := b.runners[chatID]
	b.runnersMu.RUnlock()

	if !exists {
		return
	}

	runner.mu.Lock()
	runner.sess.SetMessages(runner.ag.GetMemory())
	runner.mu.Unlock()

	if err := b.store.Save(runner.sess); err != nil {
		log.Printf("保存会话失败 (chat=%d): %v", chatID, err)
	}
}

// saveAllSessions 保存所有会话
func (b *Bot) saveAllSessions() {
	b.runnersMu.RLock()
	defer b.runnersMu.RUnlock()

	for chatID, runner := range b.runners {
		runner.mu.Lock()
		runner.sess.SetMessages(runner.ag.GetMemory())
		runner.mu.Unlock()

		if err := b.store.Save(runner.sess); err != nil {
			log.Printf("保存会话失败 (chat=%d): %v", chatID, err)
		}
	}
	log.Println("💾 所有会话已保存")
}

// ============================================================================
// 辅助函数
// ============================================================================

// escapeMarkdownV2 转义 Telegram MarkdownV2 特殊字符
// 参考: https://core.telegram.org/bots/api#markdownv2-style
// 需要转义的字符: _ * [ ] ( ) ~ ` > # + - = | { } . !
func escapeMarkdownV2(s string) string {
	// 按顺序替换，注意 \\ 必须在最前面
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}

// buildDisplayText 构建显示文本（整合内容和推理过程）
// 用户内容自动进行 MarkdownV2 转义
func buildDisplayText(content, reasoning string, tools []toolInfo) string {
	var sb strings.Builder

	// Bug 修复 #5: 推理内容中的 ``` 会破坏代码块格式，替换为三个单引号
	if reasoning != "" {
		sb.WriteString("💭 *思考过程:*\n")
		sb.WriteString("```\n")
		safeReasoning := strings.ReplaceAll(reasoning, "```", "'''")
		sb.WriteString(safeReasoning)
		sb.WriteString("\n```\n\n")
	}

	// Bug 修复 #1: LLM 输出内容进行 MarkdownV2 转义（- 等字符必须转义）
	if content != "" {
		sb.WriteString(escapeMarkdownV2(content))
	}

	// 正在执行的工具
	if len(tools) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("⏳ 正在执行: *%s*\n", escapeMarkdownV2(t.name)))
		}
	}

	return sb.String()
}

// truncateByRunes 按 rune 安全截断字符串
func truncateByRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// splitMessage 按最大 rune 数分割消息（尽量在换行处分割）
func splitMessage(text string, maxLen int) []string {
	if utf8.RuneCountInString(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	lines := strings.Split(text, "\n")

	current := ""
	for _, line := range lines {
		// 如果单行超过 maxLen，需要按 rune 再分割
		for utf8.RuneCountInString(line) > maxLen {
			runes := []rune(line)
			chunks = append(chunks, string(runes[:maxLen]))
			line = string(runes[maxLen:])
		}

		if current == "" {
			current = line
		} else if utf8.RuneCountInString(current)+1+utf8.RuneCountInString(line) > maxLen {
			chunks = append(chunks, current)
			current = line
		} else {
			current += "\n" + line
		}
	}

	if current != "" {
		chunks = append(chunks, current)
	}

	return chunks
}
