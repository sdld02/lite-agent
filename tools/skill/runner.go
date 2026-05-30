package skill

import (
	"context"
	"fmt"
	"time"

	"lite-agent/agent"
	agentpkg "lite-agent/tools/agent"
)

// DefaultSkillRunner 默认技能运行器
// 使用现有的子 Agent 系统执行 fork 模式的技能
type DefaultSkillRunner struct {
	registry  *agentpkg.ToolRegistry
	provider  agent.LLMProvider
	runner    *agentpkg.Runner
}

// NewDefaultSkillRunner 创建默认技能运行器
func NewDefaultSkillRunner(registry *agentpkg.ToolRegistry, provider agent.LLMProvider) *DefaultSkillRunner {
	return &DefaultSkillRunner{
		registry: registry,
		provider: provider,
		runner:   agentpkg.NewRunner(registry, agentpkg.LLMConfig{Provider: provider}),
	}
}

// RunSkill 执行 fork 模式的技能
func (r *DefaultSkillRunner) RunSkill(ctx context.Context, def SkillDefinition, prompt string) (*agent.ToolResult, error) {
	// 为技能构建 Agent 定义
	agentDef := agentpkg.AgentDefinition{
		AgentType:       def.Name,
		WhenToUse:       def.WhenToUse,
		Source:          agentpkg.SourceBuiltIn,
		SystemPrompt:    def.Prompt,
		Tools:           def.Tools,
		DisallowedTools: def.DisallowedTools,
		MaxTurns:        def.EffectiveMaxTurns(),
	}

	// 如果技能没有指定工具，默认使用通配符
	if len(agentDef.Tools) == 0 {
		agentDef.Tools = []string{"*"}
	}

	// 使用子 Agent 运行器执行
	startTime := time.Now()
	subResult, err := r.runner.Run(ctx, agentDef, prompt)
	if err != nil {
		return nil, fmt.Errorf("fork skill execution: %w", err)
	}

	duration := time.Since(startTime)

	// 构建结果
	resultContent := subResult.Content
	richData := map[string]interface{}{
		"agentId":           subResult.AgentID,
		"agentType":         def.Name,
		"content":           subResult.Content,
		"totalToolUseCount": subResult.TotalToolUseCount,
		"totalDurationMs":   duration.Milliseconds(),
		"status":            "completed",
		"skillName":         def.Name,
	}

	return &agent.ToolResult{
		Content:  resultContent,
		RichData: richData,
	}, nil
}
