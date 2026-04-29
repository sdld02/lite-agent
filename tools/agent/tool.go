package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AgentTool 子Agent工具
// 实现了 agent.Tool 接口，当LLM调用时启动子Agent
type AgentTool struct {
	runner      *Runner
	definitions []AgentDefinition
}

// NewAgentTool 创建 AgentTool 实例
// definitions 为初始的 Agent 定义列表（通常从 builtin 包和用户目录加载）
func NewAgentTool(runner *Runner, definitions []AgentDefinition) *AgentTool {
	return &AgentTool{
		runner:      runner,
		definitions: definitions,
	}
}

// AddDefinition 添加 Agent 定义（用于注册自定义 Agent）
func (t *AgentTool) AddDefinition(def AgentDefinition) {
	t.definitions = append(t.definitions, def)
}

// Name 工具名称
func (t *AgentTool) Name() string {
	return "agent"
}

// Description 工具描述（含可用的子Agent类型列表）
func (t *AgentTool) Description() string {
	var sb strings.Builder
	sb.WriteString("Launch a new agent to handle complex, multi-step tasks autonomously.\n\n")
	sb.WriteString("The agent tool launches specialized agents (subprocesses) that autonomously handle complex tasks. ")
	sb.WriteString("Each agent type has specific capabilities and tools available to it.\n\n")
	sb.WriteString("Available agent types and the tools they have access to:\n")

	for _, def := range t.definitions {
		toolsDesc := t.describeTools(def)
		sb.WriteString(fmt.Sprintf("- %s: %s (Tools: %s)\n", def.AgentType, def.WhenToUse, toolsDesc))
	}

	sb.WriteString("\nUsage notes:\n")
	sb.WriteString("- Always include a short description (3-5 words) summarizing what the agent will do\n")
	sb.WriteString("- When the agent is done, it will return a single message back to you\n")
	sb.WriteString("- The agent's outputs should generally be trusted\n")
	sb.WriteString("- Clearly tell the agent whether you expect it to write code or just to do research\n")
	sb.WriteString("- Launch multiple agents concurrently whenever possible to maximize performance\n")

	return sb.String()
}

// describeTools 描述 Agent 可用的工具
func (t *AgentTool) describeTools(def AgentDefinition) string {
	if len(def.DisallowedTools) > 0 {
		return fmt.Sprintf("All tools except %s", strings.Join(def.DisallowedTools, ", "))
	}
	if def.HasWildcardTools() {
		return "All tools"
	}
	return strings.Join(def.Tools, ", ")
}

// Parameters 工具参数定义
func (t *AgentTool) Parameters() map[string]interface{} {
	// 构建 agent_type 的枚举值
	agentTypes := make([]string, len(t.definitions))
	for i, def := range t.definitions {
		agentTypes[i] = def.AgentType
	}

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{
				"type":        "string",
				"description": "A short (3-5 word) description of the task",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"subagent_type": map[string]interface{}{
				"type":        "string",
				"description": "The type of specialized agent to use for this task",
				"enum":        agentTypes,
			},
		},
		"required": []string{"description", "prompt"},
	}
}

// Execute 执行 Agent 工具调用
func (t *AgentTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	// 解析参数
	description, _ := args["description"].(string)
	prompt, ok := args["prompt"].(string)
	if !ok || prompt == "" {
		return "", fmt.Errorf("prompt 参数必须是字符串且不能为空")
	}

	subagentType, _ := args["subagent_type"].(string)

	// 查找 Agent 定义
	var def AgentDefinition
	found := false

	if subagentType != "" {
		for _, d := range t.definitions {
			if d.AgentType == subagentType {
				def = d
				found = true
				break
			}
		}
		if !found {
			// 列出可用的类型
			available := make([]string, len(t.definitions))
			for i, d := range t.definitions {
				available[i] = d.AgentType
			}
			return "", fmt.Errorf("unknown agent type '%s'. Available: %s",
				subagentType, strings.Join(available, ", "))
		}
	} else {
		// 默认使用 general-purpose
		for _, d := range t.definitions {
			if d.AgentType == "general-purpose" {
				def = d
				found = true
				break
			}
		}
		if !found {
			def = t.definitions[0]
		}
	}

	// 运行子Agent
	result, err := t.runner.Run(ctx, def, prompt)
	if err != nil {
		return "", fmt.Errorf("sub-agent failed: %w", err)
	}

	// 格式化输出
	output := map[string]interface{}{
		"agentId":           result.AgentID,
		"agentType":         result.AgentType,
		"content":           result.Content,
		"totalToolUseCount": result.TotalToolUseCount,
		"totalDurationMs":   result.TotalDurationMs,
		"status":            "completed",
		"prompt":            prompt,
	}

	if description != "" {
		output["description"] = description
	}

	data, _ := json.MarshalIndent(output, "", "  ")
	return string(data), nil
}
