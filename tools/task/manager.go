package task

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

var (
	globalStore   *FileTaskStore
	globalManager *Manager
	initMu        sync.Mutex
)

// Manager 任务系统全局管理器，封装存储和 Agent 身份信息。
type Manager struct {
	Store     *FileTaskStore
	AgentInfo AgentInfo
	// TeamName 用于多 Agent 共享任务列表时的 team 标识
	TeamName string
}

// InitGlobalManager 初始化全局任务管理器（应在 agent 启动时调用一次）
func InitGlobalManager(homeDir string) *Manager {
	initMu.Lock()
	defer initMu.Unlock()

	if globalManager != nil {
		return globalManager
	}

	basePath := filepath.Join(homeDir, ".lite-agent", "tasks")
	store := NewFileTaskStore(basePath)

	// 从环境变量读取 Agent 身份
	agentInfo := AgentInfo{
		ID:    os.Getenv("LITE_AGENT_ID"),
		Name:  os.Getenv("LITE_AGENT_NAME"),
		Color: os.Getenv("LITE_AGENT_COLOR"),
	}

	globalStore = store
	globalManager = &Manager{
		Store:     store,
		AgentInfo: agentInfo,
		TeamName:  os.Getenv("LITE_TEAM_NAME"),
	}

	return globalManager
}

// GetGlobalManager 获取全局任务管理器
func GetGlobalManager() *Manager {
	return globalManager
}

// GetGlobalStore 获取全局任务存储
func GetGlobalStore() *FileTaskStore {
	return globalStore
}

// SetGlobalManager 设置全局管理器（用于测试或自定义配置）
func SetGlobalManager(mgr *Manager) {
	initMu.Lock()
	defer initMu.Unlock()
	globalManager = mgr
	if mgr != nil {
		globalStore = mgr.Store
	} else {
		globalStore = nil
	}
}

// contextKey 用于 context 传递 session ID 的键类型
type contextKey struct{}

// ContextWithSessionID 将 session ID 注入 context（用于 server 模式多连接隔离）
func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, contextKey{}, sessionID)
}

// SessionIDFromContext 从 context 中提取 session ID
func SessionIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(contextKey{}).(string); ok {
		return id
	}
	return ""
}

// GetTaskListID 获取当前的任务列表 ID。
// 优先级：
//  1. LITE_TASK_LIST_ID 环境变量
//  2. LITE_TEAM_NAME 环境变量（多 Agent 共享）
//  3. context 中的 session ID（server 模式并发安全）
//  4. LITE_SESSION_ID 环境变量（CLI 模式兼容）
//  5. "default"
func (m *Manager) GetTaskListID() string {
	if id := os.Getenv("LITE_TASK_LIST_ID"); id != "" {
		return id
	}
	if m.TeamName != "" {
		return m.TeamName
	}
	if id := os.Getenv("LITE_SESSION_ID"); id != "" {
		return id
	}
	return "default"
}

// GetTaskListIDFromCtx 从 context 获取任务列表 ID（优先使用 context 中的 session ID）
func (m *Manager) GetTaskListIDFromCtx(ctx context.Context) string {
	if id := os.Getenv("LITE_TASK_LIST_ID"); id != "" {
		return id
	}
	if m.TeamName != "" {
		return m.TeamName
	}
	if id := SessionIDFromContext(ctx); id != "" {
		return id
	}
	if id := os.Getenv("LITE_SESSION_ID"); id != "" {
		return id
	}
	return "default"
}

// IsEnabled 检查任务系统是否启用
func IsEnabled() bool {
	mgr := GetGlobalManager()
	return mgr != nil && mgr.Store != nil
}
