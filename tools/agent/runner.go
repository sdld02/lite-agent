package agent

import (
	"context"
	"fmt"
	"time"

	"lite-agent/agent"
)

// ToolFactory 工具工厂函数类型：创建新的工具实例
type ToolFactory func() agent.Tool

// ToolRegistry 工具注册表，维护工具名称到工厂函数的映射
type ToolRegistry struct {
	factories map[string]ToolFactory
}

// NewToolRegistry 创建新的工具注册表
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		factories: make(map[string]ToolFactory),
	}
}

// Register 注册工具工厂
func (r *ToolRegistry) Register(name string, factory ToolFactory) {
	r.factories[name] = factory
}

// Get 获取工具工厂
func (r *ToolRegistry) Get(name string) (ToolFactory, bool) {
	f, ok := r.factories[name]
	return f, ok
}

// AllNames 返回所有已注册的工具名称
func (r *ToolRegistry) AllNames() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	return names
}

// Runner 子Agent运行器
// 负责根据 AgentDefinition 创建并执行子 Agent
type Runner struct {
	registry  *ToolRegistry
	llmConfig LLMConfig // 子Agent 的 LLM 配置
}

// LLMConfig 子Agent 的 LLM 配置
// 子Agent 复用主Agent的 provider，但可以使用不同的 model
type LLMConfig struct {
	Provider agent.LLMProvider // 复用主Agent的 provider（共享连接/配置）
}

// NewRunner 创建子Agent运行器
func NewRunner(registry *ToolRegistry, llmConfig LLMConfig) *Runner {
	return &Runner{
		registry:  registry,
		llmConfig: llmConfig,
	}
}

// Run 同步运行子 Agent，阻塞直到完成
func (r *Runner) Run(ctx context.Context, def AgentDefinition, prompt string) (*SubAgentResult, error) {
	startTime := time.Now()

	// 创建子Agent实例
	subAgent := agent.NewAgent(r.llmConfig.Provider)

	// 设置系统提示词
	subAgent.SetSystemPrompt(def.SystemPrompt)

	// 设置最大步数
	subAgent.SetMaxSteps(def.EffectiveMaxTurns())

	// 组装工具池（根据 Agent 定义过滤）
	toolNames := r.resolveToolNames(def)
	toolUseCount := 0
	for _, name := range toolNames {
		factory, ok := r.registry.Get(name)
		if !ok {
			continue
		}
		tool := factory()
		subAgent.AddTool(tool)
	}

	// 包装 shell 工具以统计 tool use count
	// 由于我们无法直接 hook 到 agent 内部，这里采用包装方式
	// 实际上我们通过分析 memory 来统计

	// 执行子Agent
	response, err := subAgent.Run(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("sub-agent execution failed: %w", err)
	}

	// 统计工具调用次数
	memory := subAgent.GetMemory()
	for _, msg := range memory {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			toolUseCount += len(msg.ToolCalls)
		}
	}

	duration := time.Since(startTime)

	result := &SubAgentResult{
		AgentID:          fmt.Sprintf("subagent-%d", startTime.UnixMilli()),
		AgentType:        def.AgentType,
		Content:          response,
		TotalToolUseCount: toolUseCount,
		TotalDurationMs:  duration.Milliseconds(),
	}

	return result, nil
}

// resolveToolNames 根据 Agent 定义解析最终的工具名称列表
//
// 解析规则（参考 Claude Code 的 resolveAgentTools）：
//  1. 如果 Tools 为 nil 或 ["*"]：使用所有注册的工具（排除 DisallowedTools）
//  2. 否则：只使用 Tools 中指定的工具（排除 DisallowedTools）
//  3. 子Agent 默认禁止调用 agent 工具，防止无限递归
func (r *Runner) resolveToolNames(def AgentDefinition) []string {
	// 构建禁止工具集合
	disallowed := make(map[string]bool)
	for _, t := range def.DisallowedTools {
		disallowed[t] = true
	}

	// 子Agent 默认禁止 agent 工具（防止无限递归）
	disallowed["agent"] = true

	// 所有可用工具
	allTools := r.registry.AllNames()

	if def.HasWildcardTools() || len(def.Tools) == 0 {
		// 通配符模式：所有工具减去禁止工具
		var result []string
		for _, name := range allTools {
			if !disallowed[name] {
				result = append(result, name)
			}
		}
		return result
	}

	// 白名单模式：指定工具减去禁止工具
	var result []string
	for _, name := range def.Tools {
		if !disallowed[name] {
			// 验证工具是否已注册
			if _, ok := r.registry.Get(name); ok {
				result = append(result, name)
			}
		}
	}
	return result
}
