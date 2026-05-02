package server

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"lite-agent/agent"
	"lite-agent/session"
	agentpkg "lite-agent/tools/agent"

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

	// 工具工厂列表（用于为每个连接创建独立工具实例）
	toolFactories []ToolFactory

	// 连接管理
	connMu   sync.RWMutex
	conns    map[*ConnectionHandler]struct{}
	upgrader websocket.Upgrader

	// HTTP 服务器
	httpServer *http.Server
}

// NewServer 创建 WebSocket 服务实例
func NewServer(addr string, store *session.Store, registry *agentpkg.ToolRegistry,
	provider agent.LLMProvider, systemPrompt string, maxSteps int, toolFactories []ToolFactory) *Server {

	return &Server{
		addr:          addr,
		store:         store,
		registry:      registry,
		provider:      provider,
		systemPrompt:  systemPrompt,
		maxSteps:      maxSteps,
		maxConns:      20, // 默认最大连接数
		toolFactories: toolFactories,
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
		h.cancel()
		h.saveSession()
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
