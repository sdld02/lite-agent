package skill

// SKILL_TOOL_NAME Skill 工具的注册名称
const SKILL_TOOL_NAME = "skill"

// 技能目录路径常量
const (
	// UserSkillDir 用户级技能目录（~/.lite-agent/skills/）
	UserSkillDir = ".lite-agent/skills"

	// ProjectSkillDir 项目级技能目录（.lite-agent/skills/）
	ProjectSkillDir = ".lite-agent/skills"

	// SkillFileName 技能定义文件名
	SkillFileName = "SKILL.md"
)

// Skill 工具验证错误码（参考 Claude Code 的 errorCode 设计）
const (
	ErrCodeInvalidFormat            = 1 // 无效的技能名称格式
	ErrCodeUnknownSkill             = 2 // 未知技能
	ErrCodeLoadFailed               = 3 // 技能加载失败
	ErrCodeDisableModelInvocation   = 4 // 技能禁用了模型调用
	ErrCodeNotPromptSkill           = 5 // 不是基于提示词的技能
	ErrCodeNotDiscovered            = 6 // 技能未被发现
)
