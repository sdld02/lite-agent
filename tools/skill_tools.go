package tools

import (
	"lite-agent/agent"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools/skill"
)

// NewSkillTool 创建技能工具
//
// 需要提供：
//   - homeDir: 用户主目录（如 ~/），用于加载用户级技能
//   - projectRoot: 项目根目录，用于加载项目级技能
//   - registry: 工具注册表（用于 fork 模式子Agent）
//   - provider: LLM 提供者（用于 fork 模式子Agent）
func NewSkillTool(homeDir, projectRoot string, registry *agentpkg.ToolRegistry, provider agent.LLMProvider) *skill.SkillTool {
	// 1. 创建加载器
	loader := skill.NewLoader(homeDir, projectRoot)

	// 2. 创建默认技能运行器（用于 fork 模式）
	var runner skill.SkillRunner
	if registry != nil && provider != nil {
		runner = skill.NewDefaultSkillRunner(registry, provider)
	}

	// 3. 加载所有技能（内置 + 文件系统）
	skills := loader.LoadSkills(skill.BuiltinSkills)

	// 4. 创建 SkillTool
	return skill.NewSkillTool(skills, loader, runner)
}
