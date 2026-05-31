package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ClientState MCP 客户端状态
type ClientState int

const (
	StateDisconnected ClientState = iota
	StateConnecting
	StateConnected
	StateError
)

var stateNames = map[ClientState]string{
	StateDisconnected: "disconnected",
	StateConnecting:   "connecting",
	StateConnected:    "connected",
	StateError:        "error",
}

func (s ClientState) String() string {
	if n, ok := stateNames[s]; ok {
		return n
	}
	return "unknown"
}

// MCPClient 管理与一个 MCP 服务器的连接。
//
// 采用懒加载模式：调用 Connect() 时才启动子进程并完成初始化握手。
// 连接成功后缓存工具列表，后续调用直接复用。
type MCPClient struct {
	name   string
	config ServerConfig

	transport *StdioTransport

	mu       sync.Mutex
	state    ClientState
	tools    []MCPToolDefinition // 缓存工具列表（连接后填充）
	crashCnt int                 // 崩溃计数

	// 重连控制
	maxRestarts int
}

// NewClient 创建 MCP 客户端实例（不启动连接）
func NewClient(config ServerConfig) *MCPClient {
	return &MCPClient{
		name:        config.Name,
		config:      config,
		state:       StateDisconnected,
		maxRestarts: 3,
	}
}

// Connect 建立与 MCP 服务器的连接（懒加载入口）。
//
// 流程：
//  1. 启动子进程
//  2. 发送 initialize 请求
//  3. 发送 initialized 通知
//  4. 调用 tools/list 缓存工具列表
func (c *MCPClient) Connect() error {
	c.mu.Lock()
	if c.state == StateConnected {
		c.mu.Unlock()
		return nil
	}
	if c.state == StateConnecting {
		c.mu.Unlock()
		return fmt.Errorf("MCP server %s connection already in progress", c.name)
	}
	if c.crashCnt >= c.maxRestarts {
		c.mu.Unlock()
		return fmt.Errorf("MCP server %s has crashed %d times, refusing to reconnect", c.name, c.crashCnt)
	}
	c.state = StateConnecting
	c.mu.Unlock()

	// 创建 transport 并启动（不持锁）
	transport := newStdioTransport(c.name)
	if err := transport.Start(c.config.Command, c.config.Args, c.config.Env, ""); err != nil {
		c.mu.Lock()
		c.state = StateError
		c.mu.Unlock()
		return fmt.Errorf("start MCP server %s: %w", c.name, err)
	}

	// 初始化握手（不持锁）
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	initParams := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    ClientCapabilities{},
		ClientInfo: ClientInfo{
			Name:    "lite-agent",
			Version: "0.1.0",
		},
	}
	initResult, err := transport.SendRequest(ctx, MethodInitialize, initParams)
	if err != nil {
		transport.Stop()
		c.mu.Lock()
		c.state = StateError
		c.crashCnt++
		c.mu.Unlock()
		return fmt.Errorf("initialize MCP server %s: %w", c.name, err)
	}

	// 验证响应
	var initResp InitializeResult
	if err := json.Unmarshal(initResult, &initResp); err != nil {
		transport.Stop()
		c.mu.Lock()
		c.state = StateError
		c.mu.Unlock()
		return fmt.Errorf("parse initialize response from %s: %w", c.name, err)
	}

	// 发送 initialized 通知
	if err := transport.SendNotification(MethodInitialized, struct{}{}); err != nil {
		transport.Stop()
		c.mu.Lock()
		c.state = StateError
		c.mu.Unlock()
		return fmt.Errorf("send initialized notification to %s: %w", c.name, err)
	}

	// 获取工具列表并缓存
	tools, err := c.listTools(ctx, transport)
	if err != nil {
		transport.Stop()
		c.mu.Lock()
		c.state = StateError
		c.mu.Unlock()
		return fmt.Errorf("list tools from %s: %w", c.name, err)
	}

	// 全部成功后，持锁更新状态
	c.mu.Lock()
	c.transport = transport
	c.tools = tools
	c.state = StateConnected
	c.mu.Unlock()
	return nil
}

// ListTools 返回服务器提供的工具列表（从缓存）
func (c *MCPClient) ListTools() ([]MCPToolDefinition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != StateConnected {
		return nil, fmt.Errorf("MCP server %s is not connected (state: %s)", c.name, c.state)
	}

	return c.tools, nil
}

// GetCachedTools 返回已缓存的工具列表（如果已连接）。
// 第二个返回值表示是否已连接并存在缓存。
func (c *MCPClient) GetCachedTools() ([]MCPToolDefinition, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state != StateConnected {
		return nil, false
	}

	result := make([]MCPToolDefinition, len(c.tools))
	copy(result, c.tools)
	return result, true
}

// CallTool 调用 MCP 服务器的工具。
//
// 如果尚未连接，自动调用 Connect()。
func (c *MCPClient) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*CallToolResult, error) {
	// 确保已连接
	if err := c.Connect(); err != nil {
		return nil, err
	}

	params := CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	}

	resultRaw, err := c.transport.SendRequest(ctx, MethodToolsCall, params)
	if err != nil {
		// 连接错误时标记为断开，下次自动重连
		c.mu.Lock()
		c.state = StateDisconnected
		c.crashCnt++
		c.mu.Unlock()
		return nil, fmt.Errorf("call tool '%s' on %s: %w", toolName, c.name, err)
	}

	var result CallToolResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return nil, fmt.Errorf("parse tool result from %s: %w", c.name, err)
	}

	return &result, nil
}

// Disconnect 断开与 MCP 服务器的连接
func (c *MCPClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdown()
}

// IsHealthy 检查连接状态是否健康
func (c *MCPClient) IsHealthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == StateConnected
}

// State 返回当前状态
func (c *MCPClient) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Name 返回服务器名称
func (c *MCPClient) Name() string {
	return c.name
}

// ResetCrashCount 重置崩溃计数（用于手动恢复）
func (c *MCPClient) ResetCrashCount() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.crashCnt = 0
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// listTools 内部工具列表获取（不持锁，transport 由调用方提供）
func (c *MCPClient) listTools(ctx context.Context, transport *StdioTransport) ([]MCPToolDefinition, error) {
	resultRaw, err := transport.SendRequest(ctx, MethodToolsList, struct{}{})
	if err != nil {
		return nil, err
	}

	var result ListToolsResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list response: %w", err)
	}

	return result.Tools, nil
}

// shutdown 内部关闭（需持有锁）
func (c *MCPClient) shutdown() {
	if c.transport != nil {
		c.transport.Stop()
		c.transport = nil
	}
	c.state = StateDisconnected
	c.tools = nil

	// 等待一小段时间确保进程退出
	time.Sleep(200 * time.Millisecond)
}
