package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillFrontmatter SKILL.md 文件的 YAML frontmatter 结构
type SkillFrontmatter struct {
	Name               string   `yaml:"name"`
	Description        string   `yaml:"description"`
	WhenToUse          string   `yaml:"whenToUse,omitempty"`
	ArgumentHint       string   `yaml:"argumentHint,omitempty"`
	ArgNames           []string `yaml:"argNames,omitempty"`
	Context            string   `yaml:"context,omitempty"`            // "inline" 或 "fork"
	Model              string   `yaml:"model,omitempty"`
	AgentType          string   `yaml:"agentType,omitempty"`
	MaxTurns           int      `yaml:"maxTurns,omitempty"`
	Tools              []string `yaml:"tools,omitempty"`
	DisallowedTools    []string `yaml:"disallowedTools,omitempty"`
	ProgressMessage    string   `yaml:"progressMessage,omitempty"`
	IsHidden           bool     `yaml:"isHidden,omitempty"`
	DisableModelInvocation bool `yaml:"disableModelInvocation,omitempty"`
}

// Loader 技能加载器
type Loader struct {
	userDir    string // 用户级技能目录
	projectDir string // 项目级技能目录（运行时传入的工作目录）
}

// NewLoader 创建技能加载器
func NewLoader(homeDir, projectRoot string) *Loader {
	return &Loader{
		userDir:    filepath.Join(homeDir, UserSkillDir),
		projectDir: filepath.Join(projectRoot, ProjectSkillDir),
	}
}

// LoadSkills 加载所有技能（内置 + 用户 + 项目）
// 优先级：内置 > 项目 > 用户（同名技能高优先级覆盖低优先级）
func (l *Loader) LoadSkills(builtinSkills []SkillDefinition) []SkillDefinition {
	// 使用 map 去重，后加载的覆盖先加载的
	skills := make(map[string]SkillDefinition)

	// 1. 先加载用户技能（最低优先级）
	userSkills := l.loadFromDir(l.userDir, SourceUser)
	for _, s := range userSkills {
		skills[s.Name] = s
	}

	// 2. 加载项目技能（中等优先级）
	projectSkills := l.loadFromDir(l.projectDir, SourceProject)
	for _, s := range projectSkills {
		skills[s.Name] = s
	}

	// 3. 加载内置技能（最高优先级）
	for _, s := range builtinSkills {
		skills[s.Name] = s
	}

	// 转为切片，排除隐藏的
	result := make([]SkillDefinition, 0, len(skills))
	for _, s := range skills {
		result = append(result, s)
	}
	return result
}

// LoadOnlyFileSkills 只加载文件系统中的技能（用户 + 项目），排除内置技能
func (l *Loader) LoadOnlyFileSkills() []SkillDefinition {
	skills := make(map[string]SkillDefinition)

	userSkills := l.loadFromDir(l.userDir, SourceUser)
	for _, s := range userSkills {
		skills[s.Name] = s
	}

	projectSkills := l.loadFromDir(l.projectDir, SourceProject)
	for _, s := range projectSkills {
		skills[s.Name] = s
	}

	result := make([]SkillDefinition, 0, len(skills))
	for _, s := range skills {
		result = append(result, s)
	}
	return result
}

// loadFromDir 从指定目录加载所有 SKILL.md 文件
func (l *Loader) loadFromDir(dir string, source SkillSource) []SkillDefinition {
	var skills []SkillDefinition

	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录不存在是正常情况
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(dir, entry.Name())
		skillFile := filepath.Join(skillDir, SkillFileName)

		data, err := os.ReadFile(skillFile)
		if err != nil {
			// SKILL.md 不存在，跳过
			continue
		}

		skill, err := parseSkillMarkdown(data, source, skillDir)
		if err != nil {
			// 解析失败，跳过并打印警告
			fmt.Fprintf(os.Stderr, "Warning: failed to parse skill %s: %v\n", skillFile, err)
			continue
		}

		// 文件名作为默认名称
		if skill.Name == "" {
			skill.Name = entry.Name()
		}

		skills = append(skills, skill)
	}

	return skills
}

// parseSkillMarkdown 解析 SKILL.md 内容（YAML frontmatter + Markdown body）
//
// 格式：
//   ---
//   name: my-skill
//   description: 描述
//   ---
//   技能提示词内容...
func parseSkillMarkdown(data []byte, source SkillSource, baseDir string) (SkillDefinition, error) {
	content := string(data)

	// 解析 YAML frontmatter
	var frontmatter SkillFrontmatter
	var body string

	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		// 找到第二个 ---
		endIdx := strings.Index(content[4:], "---")
		if endIdx >= 0 {
			yamlContent := content[4 : endIdx+4]
			body = content[endIdx+8:] // 跳过结束的 ---\n

			if err := yaml.Unmarshal([]byte(yamlContent), &frontmatter); err != nil {
				return SkillDefinition{}, fmt.Errorf("parse frontmatter: %w", err)
			}
		} else {
			body = content
		}
	} else {
		body = content
	}

	// 确定执行上下文
	context := ContextInline
	if frontmatter.Context == "fork" {
		context = ContextFork
	}

	return SkillDefinition{
		Name:                  frontmatter.Name,
		Description:           frontmatter.Description,
		WhenToUse:             frontmatter.WhenToUse,
		Source:                source,
		Context:               context,
		Model:                 frontmatter.Model,
		AgentType:             frontmatter.AgentType,
		MaxTurns:              frontmatter.MaxTurns,
		Tools:                 frontmatter.Tools,
		DisallowedTools:       frontmatter.DisallowedTools,
		Prompt:                strings.TrimSpace(body),
		ArgumentHint:          frontmatter.ArgumentHint,
		ArgNames:              frontmatter.ArgNames,
		ProgressMessage:       frontmatter.ProgressMessage,
		IsHidden:              frontmatter.IsHidden,
		DisableModelInvocation: frontmatter.DisableModelInvocation,
		FilePath:              filepath.Join(baseDir, SkillFileName),
		BaseDir:               baseDir,
	}, nil
}

// FindSkill 按名称查找技能
func FindSkill(name string, skills []SkillDefinition) *SkillDefinition {
	// 去掉前导斜杠（兼容 /skill-name 格式）
	name = strings.TrimPrefix(name, "/")

	for i := range skills {
		if skills[i].Name == name {
			return &skills[i]
		}
	}
	return nil
}

// ListVisibleSkills 列出所有可见（非隐藏）的技能
func ListVisibleSkills(skills []SkillDefinition) []SkillDefinition {
	var result []SkillDefinition
	for _, s := range skills {
		if !s.IsHidden && !s.DisableModelInvocation {
			result = append(result, s)
		}
	}
	return result
}
