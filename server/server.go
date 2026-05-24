package server

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"lite-agent/agent"
	"lite-agent/llm"
	"lite-agent/session"
	agentpkg "lite-agent/tools/agent"
	taskpkg "lite-agent/tools/task"

	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

// ToolFactory 工具工厂函数类型
// 每次创建新连接时需要创建独立的工具实例
type ToolFactory func() agent.Tool

// Server WebSocket 服务
type Server struct {
	addr         string
	store        *session.Store
	registry     *agentpkg.ToolRegistry
	provider     agent.LLMProvider
	systemPrompt string
	maxSteps     int
	maxConns     int
	startTime    time.Time

	// LLM 配置（可在运行时通过 WebSocket 修改）
	llmCfgMu  sync.RWMutex
	llmConfig llm.OpenAIConfig

	// 工具工厂列表（用于为每个连接创建独立工具实例）
	toolFactories []ToolFactory

	// 任务管理器（用于按 session 查询任务列表）
	taskMgr *taskpkg.Manager

	// 连接管理
	connMu   sync.RWMutex
	conns    map[*ConnectionHandler]struct{}
	upgrader websocket.Upgrader

	// HTTP 服务器
	httpServer *http.Server
}

// NewServer 创建 WebSocket 服务实例
func NewServer(addr string, store *session.Store, registry *agentpkg.ToolRegistry,
	provider agent.LLMProvider, systemPrompt string, maxSteps int, toolFactories []ToolFactory,
	taskMgr *taskpkg.Manager) *Server {

	// 从 provider 提取初始配置
	cfg := llm.OpenAIConfig{}
	if openaiP, ok := provider.(*llm.OpenAIProvider); ok {
		cfg = openaiP.GetConfig()
	}

	return &Server{
		addr:          addr,
		store:         store,
		registry:      registry,
		provider:      provider,
		systemPrompt:  systemPrompt,
		maxSteps:      maxSteps,
		maxConns:      20, // 默认最大连接数
		llmConfig:     cfg,
		toolFactories: toolFactories,
		taskMgr:       taskMgr,
		startTime:     time.Now(),
		conns:         make(map[*ConnectionHandler]struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// 允许所有来源（开发环境），生产环境应配置 CheckOrigin
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Start 启动 HTTP 服务，监听并处理 WebSocket 升级请求
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleUpgrade)
	mux.HandleFunc("/health", s.handleHealth)

	// 嵌入式静态文件（HTML 控制面板）
	staticSub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(staticSub)))

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	log.Printf("🚀 WebSocket 服务已启动: ws://%s/ws", s.addr)
	log.Printf("🖥️  控制面板: http://%s/", s.addr)
	log.Printf("❤️  健康检查: http://%s/health", s.addr)

	return s.httpServer.ListenAndServe()
}

// Shutdown 优雅关闭服务
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("正在关闭 WebSocket 服务...")

	// 关闭所有活跃连接
	s.connMu.RLock()
	for h := range s.conns {
		h.saveAllSessions()
		h.cancelAllRunners()
		h.cancel()
		h.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "服务正在关闭"))
		h.conn.Close()
	}
	s.connMu.RUnlock()

	// 关闭 HTTP 服务器
	return s.httpServer.Shutdown(ctx)
}

// handleUpgrade 处理 WebSocket 升级请求
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	// 连接数限制
	if s.activeConnectionCount() >= s.maxConns {
		log.Printf("⚠️ 连接数已达上限 (%d)，拒绝新连接", s.maxConns)
		http.Error(w, "连接数已达上限", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败: %v", err)
		return
	}

	// 创建连接处理器
	handler := newConnectionHandler(conn, s)

	// 注册连接
	s.addConnection(handler)

	log.Printf("📡 新连接建立 (当前连接数: %d)", s.activeConnectionCount())

	// 启动消息处理循环（阻塞）
	handler.Run()
}

// handleHealth 健康检查端点
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","connections":%d,"uptime":"%s"}`,
		s.activeConnectionCount(),
		time.Since(s.startTime).Round(time.Second).String())
}

// addConnection 注册新连接
func (s *Server) addConnection(h *ConnectionHandler) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	s.conns[h] = struct{}{}
}

// removeConnection 移除连接
func (s *Server) removeConnection(h *ConnectionHandler) {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	delete(s.conns, h)
	log.Printf("📡 连接断开 (当前连接数: %d)", len(s.conns))
}

// activeConnectionCount 返回当前活跃连接数
func (s *Server) activeConnectionCount() int {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return len(s.conns)
}

// GetProvider 返回当前的 LLM Provider（线程安全）
func (s *Server) GetProvider() agent.LLMProvider {
	s.llmCfgMu.RLock()
	defer s.llmCfgMu.RUnlock()
	return s.provider
}

// GetLLMConfig 返回当前 LLM 配置（线程安全）
func (s *Server) GetLLMConfig() LLMConfigInfo {
	s.llmCfgMu.RLock()
	defer s.llmCfgMu.RUnlock()

	cfg := s.llmConfig
	// API Key 脱敏：只显示前4后4位
	maskedKey := cfg.APIKey
	if len(maskedKey) > 8 {
		maskedKey = maskedKey[:4] + "****" + maskedKey[len(maskedKey)-4:]
	}

	// 推断 provider 名称
	providerName := inferProviderName(cfg.BaseURL, cfg.Model)

	return LLMConfigInfo{
		Provider: providerName,
		APIKey:   maskedKey,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.Model,
	}
}

// SetLLMConfig 更新 LLM 配置（线程安全）
// 返回更新后的 LLMConfigInfo
func (s *Server) SetLLMConfig(input LLMConfigInfo) LLMConfigInfo {
	s.llmCfgMu.Lock()
	defer s.llmCfgMu.Unlock()

	// 如果传入的 API Key 包含脱敏标记（****），保留原 API Key
	if input.APIKey != "" && !containsMasked(input.APIKey) {
		s.llmConfig.APIKey = input.APIKey
	}
	if input.BaseURL != "" {
		s.llmConfig.BaseURL = input.BaseURL
	}
	if input.Model != "" {
		s.llmConfig.Model = input.Model
	}

	// 重建 provider
	s.provider = llm.NewOpenAIProvider(s.llmConfig)

	log.Printf("⚙️  LLM 配置已更新: url=%s model=%s", s.llmConfig.BaseURL, s.llmConfig.Model)

	// 直接构建返回值，不能调用 GetLLMConfig（会死锁：当前已持有写锁，GetLLMConfig 会尝试获取读锁）
	cfg := s.llmConfig
	maskedKey := cfg.APIKey
	if len(maskedKey) > 8 {
		maskedKey = maskedKey[:4] + "****" + maskedKey[len(maskedKey)-4:]
	}
	providerName := inferProviderName(cfg.BaseURL, cfg.Model)
	return LLMConfigInfo{
		Provider: providerName,
		APIKey:   maskedKey,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.Model,
	}
}

// inferProviderName 根据 URL 和 model 推断 provider 名称
func inferProviderName(baseURL, model string) string {
	switch {
	case baseURL == "":
		return "custom"
	case strings.Contains(baseURL, "api.openai.com"):
		return "openai"
	case strings.Contains(baseURL, "api.deepseek.com"):
		return "deepseek"
	case strings.Contains(baseURL, "api.moonshot.cn"):
		return "moonshot"
	case strings.Contains(baseURL, "open.bigmodel.cn"):
		return "zhipu"
	case strings.Contains(baseURL, "dashscope.aliyuncs.com"):
		return "qwen"
	case strings.Contains(baseURL, "localhost:11434"):
		return "ollama"
	default:
		return "custom"
	}
}

func containsMasked(s string) bool {
	return strings.Contains(s, "****")
}
