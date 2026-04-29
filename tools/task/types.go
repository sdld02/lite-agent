// Package task 实现 AI Agent 的任务管理系统。
//
// 设计参考 Claude Code Task 系统，支持：
//   - 基于文件系统的轻量任务存储（JSON + 文件锁）
//   - 任务状态机：pending → in_progress → completed
//   - 多 Agent 身份隔离（通过环境变量或显式设置）
//   - 任务依赖关系（blocks / blockedBy）
//   - 并发安全（进程级互斥锁 + 文件锁）
package task

// TaskStatus 任务状态枚举
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusInProgress TaskStatus = "in_progress"
	StatusCompleted  TaskStatus = "completed"
)

// AllStatuses 所有有效的任务状态
var AllStatuses = []TaskStatus{StatusPending, StatusInProgress, StatusCompleted}

// IsValidStatus 检查状态是否有效（含 deleted 用于 TaskUpdate 删除操作）
func IsValidStatus(s string) bool {
	if s == "deleted" {
		return true
	}
	for _, st := range AllStatuses {
		if string(st) == s {
			return true
		}
	}
	return false
}

// Task 任务数据模型
type Task struct {
	ID          string                 `json:"id"`          // 自增数字 ID
	Subject     string                 `json:"subject"`     // 任务标题（祈使句风格）
	Description string                 `json:"description"` // 任务详细描述
	ActiveForm  string                 `json:"activeForm,omitempty"`  // 进行中显示文本
	Owner       string                 `json:"owner,omitempty"`       // 负责人（Agent 名称）
	Status      TaskStatus             `json:"status"`      // 任务状态
	Blocks      []string               `json:"blocks"`      // 被此任务阻塞的任务 ID
	BlockedBy   []string               `json:"blockedBy"`   // 阻塞此任务的任务 ID
	Metadata    map[string]interface{} `json:"metadata,omitempty"` // 附加元数据
}

// AgentInfo Agent 身份信息
type AgentInfo struct {
	ID    string `json:"id"`    // Agent 唯一标识
	Name  string `json:"name"`  // Agent 显示名称
	Color string `json:"color"` // Agent 颜色标记
}

// AgentStatus Agent 繁忙状态
type AgentStatus struct {
	AgentID      string   `json:"agentId"`
	Name         string   `json:"name"`
	Status       string   `json:"status"` // "idle" | "busy"
	CurrentTasks []string `json:"currentTasks"`
}
