package tools

import (
	"lite-agent/agent"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools/agent/builtin"
)

// NewAgentTool 创建子Agent工具
// 需要传入 ToolRegistry 和 LLMProvider
func NewAgentTool(registry *agentpkg.ToolRegistry, provider agent.LLMProvider) *agentpkg.AgentTool {
	runner := agentpkg.NewRunner(registry, agentpkg.LLMConfig{
		Provider: provider,
	})

	// 组装内置 Agent 定义
	definitions := []agentpkg.AgentDefinition{
		builtin.GeneralPurposeAgent,
		builtin.ExploreAgent,
		builtin.PlanAgent,
	}

	return agentpkg.NewAgentTool(runner, definitions)
}

// NewToolRegistry 创建工具注册表
func NewToolRegistry() *agentpkg.ToolRegistry {
	return agentpkg.NewToolRegistry()
}
