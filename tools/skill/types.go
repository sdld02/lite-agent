// Package skill 实现技能（Skill）系统，参考 Claude Code SkillTool 设计。
//
// 核心设计理念：Skill 即工具（Skill as a Tool）
// SkillTool 是一个普通的 Tool，LLM 通过 function calling 机制发现并调用技能。
// 技能本质上是一段可复用的系统提示词，可以是简单的文本展开（inline），
// 也可以是独立的子 Agent 执行（fork）。
//
// Skill 定义来源：
//   - BuiltInSkill：硬编码在代码中的内置技能
//   - UserSkill：从 ~/.lite-agent/skills/ 加载的用户级技能
//   - ProjectSkill：从项目 .lite-agent/skills/ 加载的项目级技能
package skill

import "lite-agent/agent"

// SkillContext 技能执行上下文
type SkillContext string

const (
	ContextInline SkillContext = "inline" // 在当前对话中展开技能提示词
	ContextFork   SkillContext = "fork"   // 启动子 Agent 隔离执行
)

// SkillSource 表示技能定义的来源
type SkillSource string

const (
	SourceBuiltIn SkillSource = "built-in" // 内置技能
	SourceUser    SkillSource = "user"     // 用户级技能
	SourceProject SkillSource = "project"  // 项目级技能
)

// SkillDefinition 技能的完整定义。
//
// 对应 Claude Code 中的 Command（PromptCommand）类型联合：
//   - BuiltInSkill（source == "built-in"）
//   - UserSkill（source == "user"）
//   - ProjectSkill（source == "project"）
type SkillDefinition struct {
	// 基本标识
	Name        string      `json:"name"`        // 技能名称，如 "commit", "review-pr"
	Description string      `json:"description"` // 简短描述
	WhenToUse   string      `json:"whenToUse"`   // 告诉 LLM 何时使用此技能
	Source      SkillSource `json:"source"`      // 来源：built-in / user / project

	// 执行配置
	Context     SkillContext `json:"context,omitempty"`     // 执行模式：inline（默认）或 fork
	Model       string       `json:"model,omitempty"`       // 可选：指定使用的模型
	AgentType   string       `json:"agentType,omitempty"`   // fork 模式下的子 Agent 类型
	MaxTurns    int          `json:"maxTurns,omitempty"`    // fork 模式下最大轮数，0 表示默认
	Tools       []string     `json:"tools,omitempty"`       // fork 模式下子 Agent 可用的工具
	DisallowedTools []string `json:"disallowedTools,omitempty"` // fork 模式下禁止的工具

	// 提示词
	Prompt          string   `json:"prompt"`                    // 技能的核心提示词内容
	ArgumentHint    string   `json:"argumentHint,omitempty"`    // 参数提示，如 "<PR_NUMBER>"
	ArgNames        []string `json:"argNames,omitempty"`        // 参数名列表

	// 元数据
	ProgressMessage string `json:"progressMessage,omitempty"` // 执行时显示的消息
	IsHidden        bool   `json:"isHidden,omitempty"`        // 是否在列表中隐藏
	DisableModelInvocation bool `json:"disableModelInvocation,omitempty"` // 禁止 LLM 主动调用

	// 文件路径（用于从文件加载的技能）
	FilePath string `json:"filePath,omitempty"` // SKILL.md 文件路径
	BaseDir  string `json:"baseDir,omitempty"`  // 技能所在目录
}

// IsBuiltIn 检查是否为内置技能
func (s *SkillDefinition) IsBuiltIn() bool {
	return s.Source == SourceBuiltIn
}

// IsFork 检查是否为 fork 执行模式
func (s *SkillDefinition) IsFork() bool {
	return s.Context == ContextFork
}

// EffectiveMaxTurns 获取有效的最大轮数
func (s *SkillDefinition) EffectiveMaxTurns() int {
	if s.MaxTurns > 0 {
		return s.MaxTurns
	}
	return 50 // 默认值
}

// EffectiveModel 获取有效的模型名称
func (s *SkillDefinition) EffectiveModel() string {
	if s.Model != "" {
		return s.Model
	}
	return "inherit"
}

// ---------- 运行时结果 ----------

// SkillResult 技能执行结果（供 RichData 使用，LLM 通过 Content 读取）
type SkillResult struct {
	Success     bool   `json:"success"`
	CommandName string `json:"commandName"`
	Status      string `json:"status"` // "inline" 或 "forked"

	// inline 模式独有
	AllowedTools []string `json:"allowedTools,omitempty"`
	Model        string   `json:"model,omitempty"`

	// fork 模式独有
	AgentID string `json:"agentId,omitempty"`
	Result  string `json:"result,omitempty"` // 子 Agent 的最终输出
}

// SkillInvocation 记录一次技能调用
type SkillInvocation struct {
	SkillName string            `json:"skillName"`
	Args      string            `json:"args,omitempty"`
	AgentID   string            `json:"agentId,omitempty"` // fork 模式下的子 agent ID
	SkillContent string         `json:"skillContent,omitempty"` // 技能核心内容（用于 compaction 恢复）
	SkillPath   string          `json:"skillPath,omitempty"`    // 技能文件路径
	Messages    []agent.Message `json:"messages,omitempty"`     // 子 Agent 产生的消息
}

// NewBuiltInSkill 创建内置技能定义（便捷构造函数）
func NewBuiltInSkill(name, description, whenToUse, prompt string, context SkillContext) SkillDefinition {
	return SkillDefinition{
		Name:        name,
		Description: description,
		WhenToUse:   whenToUse,
		Source:      SourceBuiltIn,
		Context:     context,
		Prompt:      prompt,
	}
}

// NewForkSkill 创建 fork 模式的内置技能
func NewForkSkill(name, description, whenToUse, prompt, agentType string, tools []string) SkillDefinition {
	return SkillDefinition{
		Name:        name,
		Description: description,
		WhenToUse:   whenToUse,
		Source:      SourceBuiltIn,
		Context:     ContextFork,
		AgentType:   agentType,
		Tools:       tools,
		Prompt:      prompt,
	}
}
