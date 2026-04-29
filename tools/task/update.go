package task

import (
	"context"
	"encoding/json"
	"fmt"
)

// ---------------------------------------------------------------------------
// TaskUpdateTool — 更新/完成/删除任务
// ---------------------------------------------------------------------------

// TaskUpdateInput 更新任务输入
type TaskUpdateInput struct {
	TaskID      string                 `json:"taskId"`
	Subject     string                 `json:"subject,omitempty"`
	Description string                 `json:"description,omitempty"`
	ActiveForm  string                 `json:"activeForm,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Owner       string                 `json:"owner,omitempty"`
	AddBlocks   []string               `json:"addBlocks,omitempty"`
	AddBlockedBy []string              `json:"addBlockedBy,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// TaskUpdateTool 更新任务工具
type TaskUpdateTool struct{}

func NewTaskUpdateTool() *TaskUpdateTool {
	return &TaskUpdateTool{}
}

func (t *TaskUpdateTool) Name() string {
	return "task_update"
}

func (t *TaskUpdateTool) Description() string {
	return `更新任务列表中的任务。

支持的操作：
- 标记完成：status: "completed"（完成任务后使用）
- 开始工作：status: "in_progress"
- 删除任务：status: "deleted"
- 修改标题/描述：subject, description
- 分配 Owner：owner（指定负责人）
- 设置依赖：addBlocks, addBlockedBy

状态流转：pending → in_progress → completed
使用 deleted 永久删除任务。

注意：
- 只有任务完全完成时才标记为 completed
- 如遇到错误或阻塞，保持 in_progress 并创建新任务描述阻塞原因
- 标记为 completed 后，应调用 task_list 查找下一个可用任务`
}

func (t *TaskUpdateTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"taskId": map[string]interface{}{
				"type":        "string",
				"description": "要更新的任务 ID",
			},
			"subject": map[string]interface{}{
				"type":        "string",
				"description": "新标题",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "新描述",
			},
			"activeForm": map[string]interface{}{
				"type":        "string",
				"description": "进行中显示的现在进行时文本",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"pending", "in_progress", "completed", "deleted"},
				"description": "新状态",
			},
			"owner": map[string]interface{}{
				"type":        "string",
				"description": "任务负责人（agent 名称）",
			},
			"addBlocks": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "被此任务阻塞的任务 ID 列表",
			},
			"addBlockedBy": map[string]interface{}{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "阻塞此任务的任务 ID 列表",
			},
			"metadata": map[string]interface{}{
				"type":        "object",
				"description": "合并到任务 metadata（设置 null 删除对应 key）",
			},
		},
		"required": []string{"taskId"},
	}
}

func (t *TaskUpdateTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	taskID, _ := args["taskId"].(string)
	if taskID == "" {
		return "", fmt.Errorf("taskId 参数不能为空")
	}

	mgr := GetGlobalManager()
	if mgr == nil {
		return "任务系统未初始化", nil
	}

	taskListID := mgr.GetTaskListID()

	// 获取现有任务
	existing, err := mgr.Store.Get(taskListID, taskID)
	if err != nil {
		return "", fmt.Errorf("查询任务失败: %w", err)
	}
	if existing == nil {
		return fmt.Sprintf("任务 #%s 不存在", taskID), nil
	}

	updatedFields := []string{}
	updates := map[string]interface{}{}

	// 处理基本字段更新
	if v, ok := args["subject"].(string); ok && v != "" && v != existing.Subject {
		updates["subject"] = v
		updatedFields = append(updatedFields, "subject")
	}
	if v, ok := args["description"].(string); ok && v != "" && v != existing.Description {
		updates["description"] = v
		updatedFields = append(updatedFields, "description")
	}
	if v, ok := args["activeForm"].(string); ok && v != existing.ActiveForm {
		updates["activeForm"] = v
		updatedFields = append(updatedFields, "activeForm")
	}
	if v, ok := args["owner"].(string); ok && v != existing.Owner {
		updates["owner"] = v
		updatedFields = append(updatedFields, "owner")
	}
	if v, ok := args["metadata"].(map[string]interface{}); ok {
		updates["metadata"] = v
		updatedFields = append(updatedFields, "metadata")
	}

	// 处理状态更新
	if statusArg, ok := args["status"].(string); ok && statusArg != "" {
		if !IsValidStatus(statusArg) {
			return "", fmt.Errorf("无效状态: %s（有效值: pending, in_progress, completed, deleted）", statusArg)
		}

		if statusArg == "deleted" {
			err := mgr.Store.Delete(taskListID, taskID)
			if err != nil {
				return "", fmt.Errorf("删除任务失败: %w", err)
			}
			result := map[string]interface{}{
				"success":       true,
				"taskId":        taskID,
				"updatedFields": []string{"deleted"},
				"statusChange":  map[string]string{"from": string(existing.Status), "to": "deleted"},
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			return string(data), nil
		}

		if TaskStatus(statusArg) != existing.Status {
			updates["status"] = statusArg
			updatedFields = append(updatedFields, "status")
		}

		// Agent Swarm: 标记 in_progress 时自动设置 owner
		if statusArg == "in_progress" && existing.Owner == "" && mgr.AgentInfo.Name != "" {
			if _, hasOwner := updates["owner"]; !hasOwner {
				updates["owner"] = mgr.AgentInfo.Name
				updatedFields = append(updatedFields, "owner")
			}
		}
	}

	// 应用更新
	if len(updates) > 0 {
		_, err := mgr.Store.Update(taskListID, taskID, updates)
		if err != nil {
			return "", fmt.Errorf("更新任务失败: %w", err)
		}
	}

	// 处理依赖关系
	addBlocks := toStringSlice(args["addBlocks"])
	for _, blockID := range addBlocks {
		if !contains(existing.Blocks, blockID) {
			mgr.Store.BlockTask(taskListID, taskID, blockID) //nolint:errcheck
			updatedFields = append(updatedFields, "blocks")
		}
	}

	addBlockedBy := toStringSlice(args["addBlockedBy"])
	for _, blockerID := range addBlockedBy {
		if !contains(existing.BlockedBy, blockerID) {
			mgr.Store.BlockTask(taskListID, blockerID, taskID) //nolint:errcheck
			updatedFields = append(updatedFields, "blockedBy")
		}
	}

	// 构建结果
	result := map[string]interface{}{
		"success":       true,
		"taskId":        taskID,
		"updatedFields": updatedFields,
	}

	if statusArg, ok := args["status"].(string); ok && statusArg != "" &&
		statusArg != "deleted" && TaskStatus(statusArg) != existing.Status {
		result["statusChange"] = map[string]string{
			"from": string(existing.Status),
			"to":   statusArg,
		}
	}

	// 如果标记完成，提示检查 task_list
	if statusArg, ok := args["status"].(string); ok && statusArg == "completed" {
		result["hint"] = "任务已标记为完成。调用 task_list 查找下一个可用任务或查看你的工作是否解除了其他任务的阻塞。"
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return string(data), nil
}


