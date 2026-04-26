package lsp

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// ServerState 服务器状态
type ServerState string

const (
	StateStopped  ServerState = "stopped"
	StateStarting ServerState = "starting"
	StateRunning  ServerState = "running"
	StateError    ServerState = "error"
)

// ServerInstance 管理单个 LSP 服务器的完整生命周期。
//
// 状态机：
//
//	stopped → starting → running
//	    ↓                  ↓
//	  error  ←──────────  error
type ServerInstance struct {
	Name       string
	Config     *LspServerConfig
	State      ServerState
	client     *LSPClient
	startTime  time.Time
	lastError  error
	crashCount int
}

// NewServerInstance 创建服务器实例（惰性，不立即启动）
func NewServerInstance(name string, config *LspServerConfig) *ServerInstance {
	return &ServerInstance{
		Name:   name,
		Config: config,
		State:  StateStopped,
		client: NewLSPClient(name),
	}
}

// Start 启动 LSP 服务器并完成初始化握手
func (s *ServerInstance) Start(workDir string) error {
	if s.State == StateRunning || s.State == StateStarting {
		return nil
	}

	// 如果进程还在运行但状态被错误标记，直接恢复
	if s.client.IsRunning() && s.client.IsInitialized() {
		s.State = StateRunning
		return nil
	}

	// 尝试重启客户端（如果是 stopped/error 但进程存活则先 stop）
	if s.client.IsRunning() {
		s.client.Stop()
	}

	// 崩溃恢复次数限制
	maxRestarts := 3
	if s.State == StateError && s.crashCount > maxRestarts {
		err := fmt.Errorf("LSP server '%s' exceeded max crash recovery attempts (%d)", s.Name, maxRestarts)
		s.lastError = err
		return err
	}

	s.State = StateStarting

	// 启动子进程
	if err := s.client.Start(s.Config.Command, s.Config.Args, s.Config.Env, workDir); err != nil {
		s.State = StateError
		s.lastError = err
		return err
	}

	// 构建 initialize 参数
	initParams := s.buildInitParams(workDir)

	// 发送 initialize 请求（带超时）
	type initResult struct {
		resp *jsonrpcResponse
		err  error
	}
	resultCh := make(chan initResult, 1)
	go func() {
		resp, err := s.client.Initialize(initParams)
		resultCh <- initResult{resp, err}
	}()

	timeout := time.Duration(s.Config.StartupTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			s.client.Stop()
			s.State = StateError
			s.lastError = fmt.Errorf("initialize failed for %s: %w", s.Name, result.err)
			return s.lastError
		}
	case <-time.After(timeout):
		s.client.Stop()
		s.State = StateError
		s.lastError = fmt.Errorf("LSP server '%s' timed out after %v during initialization", s.Name, timeout)
		return s.lastError
	}

	// 注册 workspace/configuration 处理器
	s.client.OnRequest("workspace/configuration", func(params json.RawMessage) (interface{}, error) {
		// 返回空配置，满足协议要求
		return nil, nil
	})

	s.State = StateRunning
	s.startTime = time.Now()
	s.crashCount = 0

	return nil
}

// Stop 停止 LSP 服务器
func (s *ServerInstance) Stop() error {
	if s.State == StateStopped {
		return nil
	}
	if err := s.client.Stop(); err != nil {
		s.State = StateError
		s.lastError = err
		return err
	}
	s.State = StateStopped
	return nil
}

// SendRequest 发送 LSP 请求，带 ContentModified 重试
//
// 对 rust-analyzer 等服务器在索引期间的瞬时错误（-32801），
// 使用指数退避自动重试（最多 3 次：500ms → 1s → 2s）。
func (s *ServerInstance) SendRequest(method string, params interface{}) (json.RawMessage, error) {
	if s.State != StateRunning || !s.client.IsInitialized() {
		return nil, fmt.Errorf("LSP server '%s' is not healthy (state=%s, initialized=%v)",
			s.Name, s.State, s.client.IsInitialized())
	}

	const maxRetries = 3
	const baseDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := s.client.SendRequest(method, params)
		if err == nil {
			return result, nil
		}

		lastErr = err

		// 检查是否是 ContentModified 瞬时错误
		if rpcErr, ok := err.(*jsonrpcError); ok && rpcErr.isContentModified() && attempt < maxRetries {
			delay := baseDelay * time.Duration(math.Pow(2, float64(attempt)))
			time.Sleep(delay)
			continue
		}
		break
	}

	return nil, fmt.Errorf("LSP request '%s' failed for server '%s': %w", method, s.Name, lastErr)
}

// SendNotification 发送 LSP 通知
func (s *ServerInstance) SendNotification(method string, params interface{}) error {
	if s.State != StateRunning || !s.client.IsInitialized() {
		return fmt.Errorf("LSP server '%s' is not healthy", s.Name)
	}
	return s.client.SendNotification(method, params)
}

// IsHealthy 检查服务器是否健康
func (s *ServerInstance) IsHealthy() bool {
	return s.State == StateRunning && s.client.IsInitialized()
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// buildInitParams 构建 LSP initialize 参数
func (s *ServerInstance) buildInitParams(workDir string) map[string]interface{} {
	initOpts := s.Config.InitializationOptions
	if initOpts == nil {
		initOpts = map[string]interface{}{}
	}

	return map[string]interface{}{
		"processId":             nil, // 使用 nil 表示父进程 PID
		"rootPath":              workDir,
		"rootUri":               pathToURI(workDir),
		"workspaceFolders": []map[string]interface{}{
			{
				"uri":  pathToURI(workDir),
				"name": "workspace",
			},
		},
		"initializationOptions": initOpts,
		"capabilities": map[string]interface{}{
			"workspace": map[string]interface{}{
				"configuration":  false,
				"workspaceFolders": false,
			},
			"textDocument": map[string]interface{}{
				"synchronization": map[string]interface{}{
					"dynamicRegistration": false,
					"willSave":            false,
					"willSaveWaitUntil":   false,
					"didSave":             true,
				},
				"publishDiagnostics": map[string]interface{}{
					"relatedInformation":    true,
					"versionSupport":        false,
					"codeDescriptionSupport": true,
				},
				"hover": map[string]interface{}{
					"dynamicRegistration": false,
					"contentFormat":       []string{"markdown", "plaintext"},
				},
				"definition": map[string]interface{}{
					"dynamicRegistration": false,
					"linkSupport":         true,
				},
				"references": map[string]interface{}{
					"dynamicRegistration": false,
				},
				"documentSymbol": map[string]interface{}{
					"dynamicRegistration":               false,
					"hierarchicalDocumentSymbolSupport": true,
				},
				"implementation": map[string]interface{}{
					"dynamicRegistration": false,
					"linkSupport":         true,
				},
				"callHierarchy": map[string]interface{}{
					"dynamicRegistration": false,
				},
			},
			"general": map[string]interface{}{
				"positionEncodings": []string{"utf-16"},
			},
		},
	}
}
