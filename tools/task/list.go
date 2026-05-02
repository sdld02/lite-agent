package task

import (
	"context"
	"encoding/json"
	"fmt"

	"lite-agent/agent"
)

// ---------------------------------------------------------------------------
// TaskListTool — 列出所有任务
// ---------------------------------------------------------------------------

// TaskListTool 列出任务工具
type TaskListTool struct{}

func NewTaskListTool() *TaskListTool {
	return &TaskListTool{}
}

func (t *TaskListTool) Name() string {
	return "task_list"
}

func (t *TaskListTool) Description() string {
	return `列出当前任务列表中的所有任务。

返回每个任务的 ID、标题、状态、负责人和阻塞关系。
已完成的任务的阻塞引用会被过滤掉。

使用场景：
- 开始工作前查看所有任务
- 完成任务后查找下一个可用任务
- 检查任务依赖关系`
}

func (t *TaskListTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *TaskListTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	mgr := GetGlobalManager()
	if mgr == nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("任务系统未初始化")), IsError: true}, nil
	}

	taskListID := mgr.GetTaskListID()
	tasks, err := mgr.Store.List(taskListID)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(err), IsError: true}, nil
	}

	// 过滤 _internal metadata 的任务
	var filtered []Task
	for _, task := range tasks {
		if task.Metadata != nil {
			if _, ok := task.Metadata["_internal"]; ok {
				continue
			}
		}
		filtered = append(filtered, task)
	}

	// 构建已完成任务 ID 集合（用于过滤 blockedBy）
	resolvedIDs := make(map[string]bool)
	for _, task := range filtered {
		if task.Status == StatusCompleted {
			resolvedIDs[task.ID] = true
		}
	}

	type taskSummary struct {
		ID        string   `json:"id"`
		Subject   string   `json:"subject"`
		Status    string   `json:"status"`
		Owner     string   `json:"owner,omitempty"`
		BlockedBy []string `json:"blockedBy"`
	}

	summaries := make([]taskSummary, 0, len(filtered))
	for _, task := range filtered {
		// 过滤已完成任务的阻塞
		activeBlockedBy := make([]string, 0)
		for _, bid := range task.BlockedBy {
			if !resolvedIDs[bid] {
				activeBlockedBy = append(activeBlockedBy, bid)
			}
		}

		summaries = append(summaries, taskSummary{
			ID:        task.ID,
			Subject:   task.Subject,
			Status:    string(task.Status),
			Owner:     task.Owner,
			BlockedBy: activeBlockedBy,
		})
	}

	richData := map[string]interface{}{"tasks": summaries}
	data, _ := json.MarshalIndent(richData, "", "  ")

	// 任务列表本身是 LLM 需要的信息，直接返回 JSON
	return &agent.ToolResult{
		Content:  string(data),
		RichData: richData,
	}, nil
}
