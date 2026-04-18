package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
)

// CalculatorTool 计算器工具
type CalculatorTool struct{}

func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{}
}

func (t *CalculatorTool) Name() string {
	return "calculator"
}

func (t *CalculatorTool) Description() string {
	return "执行数学计算表达式，支持加减乘除等基本运算。例如: 2+2, 10*5, 100/4"
}

func (t *CalculatorTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "数学表达式，如: 123+456",
			},
		},
		"required": []string{"expression"},
	}
}

func (t *CalculatorTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	expression, ok := args["expression"].(string)
	if !ok {
		return "", fmt.Errorf("expression 参数必须是字符串")
	}

	// 使用表达式计算器（简化版，实际使用需要更安全的实现）
	// 这里用 node 或 python 来计算
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-Command", expression)
	} else {
		cmd = exec.CommandContext(ctx, "python3", "-c", fmt.Sprintf("print(%s)", expression))
	}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("计算失败: %v", err)
	}

	return fmt.Sprintf("计算结果: %s = %s", expression, string(output)), nil
}

// SystemInfoTool 系统信息工具
type SystemInfoTool struct{}

func NewSystemInfoTool() *SystemInfoTool {
	return &SystemInfoTool{}
}

func (t *SystemInfoTool) Name() string {
	return "system_info"
}

func (t *SystemInfoTool) Description() string {
	return "获取当前系统信息，包括操作系统、架构等"
}

func (t *SystemInfoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *SystemInfoTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	info := map[string]interface{}{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"cpus":    runtime.NumCPU(),
		"version": runtime.Version(),
	}

	result, _ := json.MarshalIndent(info, "", "  ")
	return string(result), nil
}

// TimeTool 时间查询工具
type TimeTool struct{}

func NewTimeTool() *TimeTool {
	return &TimeTool{}
}

func (t *TimeTool) Name() string {
	return "current_time"
}

func (t *TimeTool) Description() string {
	return "获取当前日期和时间"
}

func (t *TimeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *TimeTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return fmt.Sprintf("当前时间: %s", runtime.GOOS), nil
}
