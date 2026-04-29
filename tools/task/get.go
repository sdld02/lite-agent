package task

import (
	"context"
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// TaskGetTool — 获取单个任务详情
// ---------------------------------------------------------------------------

// TaskGetTool 获取任务工具
type TaskGetTool struct{}

func NewTaskGetTool() *TaskGetTool {
	return &TaskGetTool{}
}

func (t *TaskGetTool) Name() string {
	return "task_get"
}

func (t *TaskGetTool) Description() string {
	return `获取指定任务的完整详情，包括描述、状态、负责人和依赖关系。

在更新任务前，应使用此工具读取任务的最新状态。`
}

func (t *TaskGetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"taskId": map[string]interface{}{
				"type":        "string",
				"description": "要查询的任务 ID",
			},
		},
		"required": []string{"taskId"},
	}
}

func (t *TaskGetTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	taskID, _ := args["taskId"].(string)
	if taskID == "" {
		return "", fmt.Errorf("taskId 参数不能为空")
	}

	mgr := GetGlobalManager()
	if mgr == nil {
		return "任务系统未初始化", nil
	}

	taskListID := mgr.GetTaskListID()
	task, err := mgr.Store.Get(taskListID, taskID)
	if err != nil {
		return "", fmt.Errorf("查询任务失败: %w", err)
	}
	if task == nil {
		return fmt.Sprintf("任务 #%s 不存在", taskID), nil
	}

	// 过滤已完成的阻塞
	tasks, _ := mgr.Store.List(taskListID)
	resolvedIDs := make(map[string]bool)
	for _, t := range tasks {
		if t.Status == StatusCompleted {
			resolvedIDs[t.ID] = true
		}
	}

	activeBlockedBy := make([]string, 0)
	for _, bid := range task.BlockedBy {
		if !resolvedIDs[bid] {
			activeBlockedBy = append(activeBlockedBy, bid)
		}
	}

	result := map[string]interface{}{
		"task": map[string]interface{}{
			"id":          task.ID,
			"subject":     task.Subject,
			"description": task.Description,
			"status":      task.Status,
			"owner":       task.Owner,
			"blocks":      task.Blocks,
			"blockedBy":   activeBlockedBy,
			"metadata":    task.Metadata,
		},
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return string(data), nil
}
