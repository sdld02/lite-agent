package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// Manager 管理多个 MCP 服务器连接。
//
// 采用全局单例模式（参考 tools/lsp/manager.go），
// 整个进程共享一个 Manager 实例。
//
// 核心特性：
//   - 按需连接：服务器在首次调用时才启动（懒加载）
//   - 连接缓存：已连接的服务器复用连接
//   - 工具缓存：每个服务器的工具列表在连接后缓存
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*MCPClient // serverName → client
	configs []ServerConfig        // 原始配置
}

// NewManager 创建管理器实例
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*MCPClient),
	}
}

// Initialize 加载配置，创建客户端实例（但不启动连接）
func (m *Manager) Initialize(configs []ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.configs = configs

	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		if _, exists := m.clients[cfg.Name]; !exists {
			m.clients[cfg.Name] = NewClient(cfg)
			log.Printf("[MCP] 已注册服务器: %s (command: %s)", cfg.Name, cfg.Command)
		}
	}
}

// GetServerDescriptions 返回所有已配置服务器的描述信息（给系统提示词用）
func (m *Manager) GetServerDescriptions() []ServerDescription {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var descs []ServerDescription
	for _, cfg := range m.configs {
		descs = append(descs, ServerDescription{
			Name:    cfg.Name,
			Command: cfg.Command,
		})
	}
	return descs
}

// GetOrConnect 按需获取或连接 MCP 服务器。
//
// 如果服务器尚未连接，自动调用 Connect()。
func (m *Manager) GetOrConnect(serverName string) (*MCPClient, error) {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unknown MCP server: %s", serverName)
	}

	// 按需连接
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("connect to MCP server %s: %w", serverName, err)
	}

	return client, nil
}

// ListTools 获取指定服务器的工具列表
func (m *Manager) ListTools(serverName string) ([]MCPToolDefinition, error) {
	client, err := m.GetOrConnect(serverName)
	if err != nil {
		return nil, err
	}
	return client.ListTools()
}

// CallTool 调用指定服务器的工具
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, arguments map[string]interface{}) (*CallToolResult, error) {
	client, err := m.GetOrConnect(serverName)
	if err != nil {
		return nil, err
	}
	return client.CallTool(ctx, toolName, arguments)
}

// ListServers 返回所有已配置的服务器名称和状态
func (m *Manager) ListServers() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string)
	for name, client := range m.clients {
		result[name] = client.State().String()
	}
	return result
}

// IsConfigured 检查服务器是否已配置
func (m *Manager) IsConfigured(serverName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.clients[serverName]
	return exists
}

// HasServers 是否有任何 MCP 服务器配置
func (m *Manager) HasServers() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients) > 0
}

// GetConfigs 返回当前所有服务器配置（只读副本）
func (m *Manager) GetConfigs() []ServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ServerConfig, len(m.configs))
	copy(result, m.configs)
	return result
}

// GetCachedTools 返回指定服务器已缓存的工具列表。
// 第二个返回值表示该服务器是否已注册并已连接。
func (m *Manager) GetCachedTools(serverName string) ([]MCPToolDefinition, bool) {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	m.mu.RUnlock()

	if !exists {
		return nil, false
	}
	return client.GetCachedTools()
}

// Reload 热重载 MCP 配置。
//
// 行为：
//   - 断开并移除不再存在于新配置中的服务器
//   - 断开配置发生变化的服务器（下次调用时重新连接新配置）
//   - 注册全新服务器（懒加载，不立即连接）
//   - 更新内部 configs
func (m *Manager) Reload(configs []ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 构建新配置 map，方便查找
	newCfgMap := make(map[string]ServerConfig, len(configs))
	for _, cfg := range configs {
		newCfgMap[cfg.Name] = cfg
	}

	// 1. 断开并移除已删除、配置变更或禁用的服务器
	for name, client := range m.clients {
		newCfg, exists := newCfgMap[name]
		if !exists || newCfg.Disabled {
			log.Printf("[MCP] Reload: 移除服务器 %s", name)
			client.Disconnect()
			delete(m.clients, name)
			continue
		}
		// 比较配置是否变化（通过 JSON 序列化）
		oldJSON, _ := json.Marshal(client.config)
		newJSON, _ := json.Marshal(newCfg)
		if string(oldJSON) != string(newJSON) {
			log.Printf("[MCP] Reload: 重置服务器 %s（配置已变更）", name)
			client.Disconnect()
			delete(m.clients, name)
		}
	}

	// 2. 注册新增且启用的服务器
	for _, cfg := range configs {
		if cfg.Disabled {
			continue
		}
		if _, exists := m.clients[cfg.Name]; !exists {
			m.clients[cfg.Name] = NewClient(cfg)
			log.Printf("[MCP] Reload: 注册服务器 %s (command: %s)", cfg.Name, cfg.Command)
		}
	}

	m.configs = configs
	log.Printf("[MCP] Reload 完成，当前服务器数: %d", len(m.clients))
}

// Shutdown 断开所有连接
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, client := range m.clients {
		log.Printf("[MCP] 关闭服务器: %s", name)
		client.Disconnect()
	}
	m.clients = make(map[string]*MCPClient)
}

// ---------------------------------------------------------------------------
// 全局单例
// ---------------------------------------------------------------------------

var (
	globalManager *Manager
	globalMu      sync.RWMutex
)

// GetGlobalManager 获取全局 MCP 管理器实例
func GetGlobalManager() *Manager {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalManager
}

// SetGlobalManager 设置全局 MCP 管理器实例
func SetGlobalManager(m *Manager) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalManager = m
}

// InitGlobalManager 初始化全局 MCP 管理器
func InitGlobalManager(configs []ServerConfig) *Manager {
	m := NewManager()
	m.Initialize(configs)
	SetGlobalManager(m)
	return m
}
