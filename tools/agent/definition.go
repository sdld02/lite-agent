// Package agent 实现子Agent系统，参考 Claude Code AgentTool 设计。
//
// 核心设计理念：Agent 即工具（Agent as a Tool）
// AgentTool 是一个普通的 Tool，LLM 通过 function calling 机制决定何时调用它。
// 当被调用时，AgentTool 启动一个子 Agent（复用 agent.Agent 引擎），
// 子 Agent 拥有独立的系统提示词、工具池和最大轮数限制。
//
// Agent 定义体系：
//   - BuiltInAgent：硬编码在代码中的内置 Agent
//   - CustomAgent：从 .md 文件加载的自定义 Agent
//   - 所有 Agent 共享统一的 AgentDefinition 字段
package agent

import (
	"lite-agent/agent"
)

// AgentSource 表示 Agent 定义的来源
type AgentSource string

const (
	SourceBuiltIn AgentSource = "built-in"
	SourceUser    AgentSource = "user"
	SourceProject AgentSource = "project"
)

// AgentDefinition 子Agent 的完整定义。
//
// 对应 Claude Code 中的 AgentDefinition 联合类型：
//   - BuiltInAgentDefinition（source == "built-in"）
//   - CustomAgentDefinition（source == "user"/"project"）
type AgentDefinition struct {
	// 基本标识
	AgentType string      `json:"agentType"` // 唯一类型标识，如 "general-purpose", "Explore"
	WhenToUse string      `json:"whenToUse"` // 告诉 LLM 何时使用此 Agent
	Source    AgentSource `json:"source"`    // 来源：built-in / user / project

	// 工具控制
	Tools          []string `json:"tools,omitempty"`          // 允许使用的工具列表，["*"] 表示所有
	DisallowedTools []string `json:"disallowedTools,omitempty"` // 禁止使用的工具列表

	// 系统提示词
	SystemPrompt string `json:"systemPrompt"` // 子 Agent 的系统提示词

	// 模型配置
	Model string `json:"model,omitempty"` // 使用的模型，"inherit" 表示继承父 Agent

	// 执行控制
	PermissionMode string `json:"permissionMode,omitempty"` // 权限模式：acceptEdits / plan / bypassPermissions
	MaxTurns       int    `json:"maxTurns,omitempty"`       // 最大对话轮数，0 表示默认 50
	Background     bool   `json:"background,omitempty"`     // 是否强制后台运行

	// 元数据
	Color    string `json:"color,omitempty"`    // Agent 颜色标记
	Filename string `json:"filename,omitempty"` // 文件名（不含扩展名，用于自定义 Agent）
	BaseDir  string `json:"baseDir,omitempty"`  // 文件所在目录

	// 可选功能
	Skills    []string `json:"skills,omitempty"`    // 预加载的技能名称
	Memory    string   `json:"memory,omitempty"`    // 持久化记忆作用域：user / project / local
	Isolation string   `json:"isolation,omitempty"` // 隔离模式：worktree（git worktree）
}

// NewBuiltInAgent 创建内置 Agent 定义（便捷构造函数）
func NewBuiltInAgent(agentType, whenToUse, systemPrompt string, tools []string, disallowedTools []string) AgentDefinition {
	return AgentDefinition{
		AgentType:       agentType,
		WhenToUse:       whenToUse,
		Source:          SourceBuiltIn,
		Tools:           tools,
		DisallowedTools: disallowedTools,
		SystemPrompt:    systemPrompt,
		BaseDir:         "built-in",
	}
}

// HasWildcardTools 检查是否允许所有工具
func (a *AgentDefinition) HasWildcardTools() bool {
	return len(a.Tools) == 1 && a.Tools[0] == "*"
}

// IsBuiltIn 检查是否为内置 Agent
func (a *AgentDefinition) IsBuiltIn() bool {
	return a.Source == SourceBuiltIn
}

// EffectiveMaxTurns 获取有效的最大轮数
func (a *AgentDefinition) EffectiveMaxTurns() int {
	if a.MaxTurns > 0 {
		return a.MaxTurns
	}
	return 50 // 默认值
}

// ---------- 运行时 Agent 实例 ----------

// SubAgentInstance 表示一个正在运行的子 Agent 实例
type SubAgentInstance struct {
	ID           string          `json:"id"`
	AgentType    string          `json:"agentType"`
	Description  string          `json:"description"`
	Prompt       string          `json:"prompt"`
	Status       string          `json:"status"` // running / completed / failed / killed
	StartTime    int64           `json:"startTime"`
	EndTime      int64           `json:"endTime,omitempty"`
	Messages     []agent.Message `json:"messages,omitempty"`
	Result       *SubAgentResult `json:"result,omitempty"`
	AbortFunc    func()          `json:"-"` // 取消函数，不序列化
}

// SubAgentResult 子 Agent 的运行结果
type SubAgentResult struct {
	AgentID          string `json:"agentId"`
	AgentType        string `json:"agentType,omitempty"`
	Content          string `json:"content"`          // 子 Agent 的最终文本输出
	TotalToolUseCount int   `json:"totalToolUseCount"` // 工具调用总次数
	TotalDurationMs  int64  `json:"totalDurationMs"`  // 总耗时（毫秒）
	TotalTokens      int    `json:"totalTokens"`      // 总 token 消耗
}
