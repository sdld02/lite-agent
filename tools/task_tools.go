package tools

import (
	"lite-agent/tools/task"
)

// NewTaskCreateTool 创建任务创建工具
func NewTaskCreateTool() *task.TaskCreateTool {
	return task.NewTaskCreateTool()
}

// NewTaskUpdateTool 创建任务更新工具
func NewTaskUpdateTool() *task.TaskUpdateTool {
	return task.NewTaskUpdateTool()
}

// NewTaskListTool 创建任务列表工具
func NewTaskListTool() *task.TaskListTool {
	return task.NewTaskListTool()
}

// NewTaskGetTool 创建任务获取工具
func NewTaskGetTool() *task.TaskGetTool {
	return task.NewTaskGetTool()
}

// InitTaskManager 初始化任务管理器（应在 agent 启动时调用）
func InitTaskManager(homeDir string) *task.Manager {
	return task.InitGlobalManager(homeDir)
}
