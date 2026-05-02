package task

import (
	"context"
	"encoding/json"
	"fmt"

	"lite-agent/agent"
)

// ---------------------------------------------------------------------------
// TaskCreateTool — 创建新任务
// ---------------------------------------------------------------------------

// TaskCreateInput 创建任务输入
type TaskCreateInput struct {
	Subject     string                 `json:"subject"`
	Description string                 `json:"description"`
	ActiveForm  string                 `json:"activeForm,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// TaskCreateTool 创建任务工具
type TaskCreateTool struct{}

func NewTaskCreateTool() *TaskCreateTool {
	return &TaskCreateTool{}
}

func (t *TaskCreateTool) Name() string {
	return "task_create"
}

func (t *TaskCreateTool) Description() string {
	return `在任务列表中创建新任务。用于跟踪进度、组织复杂工作并向用户展示完成度。

使用场景：
- 复杂多步骤任务（3 步以上）
- 用户明确提出使用任务列表
- plan 模式下跟踪工作
- 收到新指令时立即捕获为任务

注意：单个直接任务不需要使用此工具，直接执行即可。`
}

func (t *TaskCreateTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"subject": map[string]interface{}{
				"type":        "string",
				"description": "任务标题（祈使句风格，如 '修复登录流程中的认证 bug'）",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "任务详细描述，需要做什么",
			},
			"activeForm": map[string]interface{}{
				"type":        "string",
				"description": "进行中时显示的现在进行时文本（如 '正在修复认证 bug'），可选",
			},
			"metadata": map[string]interface{}{
				"type":        "object",
				"description": "附加元数据（键值对），可选",
			},
		},
		"required": []string{"subject", "description"},
	}
}

func (t *TaskCreateTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	subject, _ := args["subject"].(string)
	description, _ := args["description"].(string)
	activeForm, _ := args["activeForm"].(string)

	if subject == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("subject 参数不能为空"), IsError: true}, nil
	}
	if description == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("description 参数不能为空"), IsError: true}, nil
	}

	mgr := GetGlobalManager()
	if mgr == nil {
		return &agent.ToolResult{Content: "任务系统未初始化，请先设置任务管理器", IsError: true}, nil
	}

	taskListID := mgr.GetTaskListID()

	// 自动设置 owner（如果配置了 agent name）
	owner := mgr.AgentInfo.Name

	task := Task{
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      StatusPending,
		Owner:       owner,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}

	// 处理 metadata
	if md, ok := args["metadata"].(map[string]interface{}); ok {
		task.Metadata = md
	}

	taskID, err := mgr.Store.Create(taskListID, task)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("创建任务失败: %w", err)), IsError: true}, nil
	}

	result := map[string]interface{}{
		"task": map[string]string{
			"id":      taskID,
			"subject": subject,
		},
		"owner": owner,
	}

	data, _ := json.MarshalIndent(result, "", "  ")
	return &agent.ToolResult{Content: string(data)}, nil
}
