package skill

import (
	"context"
	"fmt"
	"strings"

	"lite-agent/agent"
)

// SkillTool 技能工具
//
// 实现了 agent.Tool 接口。
// LLM 可以调用此工具来执行已注册的技能。
// 技能有两种执行模式：
//   - inline：在当前对话中展开技能提示词（返回给 LLM）
//   - fork：启动子 Agent 隔离执行
type SkillTool struct {
	skills    []SkillDefinition // 所有已加载的技能定义
	loader    *Loader           // 技能加载器
	runner    SkillRunner      // fork 模式下的子 Agent 运行器（可为 nil）
	invocations []SkillInvocation // 记录技能调用历史
}

// SkillRunner fork 模式的子 Agent 运行器接口
type SkillRunner interface {
	RunSkill(ctx context.Context, def SkillDefinition, args string) (*agent.ToolResult, error)
}

// NewSkillTool 创建 SkillTool 实例
func NewSkillTool(skills []SkillDefinition, loader *Loader, runner SkillRunner) *SkillTool {
	return &SkillTool{
		skills:      skills,
		loader:      loader,
		runner:      runner,
		invocations: make([]SkillInvocation, 0),
	}
}

// Name 工具名称
func (t *SkillTool) Name() string {
	return SKILL_TOOL_NAME
}

// Description 工具描述
func (t *SkillTool) Description() string {
	return `Execute a skill within the main conversation.

When users ask you to perform tasks, check if any of the available skills match. Skills provide specialized capabilities and domain knowledge.

When users reference a "slash command" or "/<something>" (e.g., "/commit", "/review-pr"), they are referring to a skill. Use this tool to invoke it.

How to invoke:
- Use this tool with the skill name and optional arguments
- Examples:
  - skill: "pdf" - invoke the pdf skill
  - skill: "commit", args: "-m 'Fix bug'" - invoke with arguments
  - skill: "review-pr", args: "123" - invoke with arguments

Important:
- Available skills are listed in the conversation
- When a skill matches the user's request, invoke the relevant Skill tool BEFORE generating any other response
- NEVER mention a skill without actually calling this tool
- Do not invoke a skill that is already running`
}

// Parameters 工具参数定义
func (t *SkillTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"skill": map[string]interface{}{
				"type":        "string",
				"description": `The skill name. E.g., "commit", "review-pr", or "pdf"`,
			},
			"args": map[string]interface{}{
				"type":        "string",
				"description": "Optional arguments for the skill",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 调用 commit 技能生成提交信息",
			},
		},
		"required": []string{"skill", "intent"},
	}
}

// Execute 执行技能工具调用
func (t *SkillTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 解析参数
	skill, _ := args["skill"].(string)
	skillArgs, _ := args["args"].(string)

	// 去掉前导斜杠（兼容 /skill-name 格式）
	skill = strings.TrimSpace(skill)
	skill = strings.TrimPrefix(skill, "/")

	if skill == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("skill 参数不能为空"),
			IsError: true,
		}, nil
	}

	// 查找技能定义
	def := FindSkill(skill, t.skills)
	if def == nil {
		// 尝试重新加载文件系统中的技能
		if t.loader != nil {
			fileSkills := t.loader.LoadOnlyFileSkills()
			def = FindSkill(skill, fileSkills)
		}
		if def == nil {
			// 列出可用的技能名
			available := make([]string, 0, len(t.skills))
			for _, s := range ListVisibleSkills(t.skills) {
				available = append(available, s.Name)
			}
			return &agent.ToolResult{
				Content: agent.FormatToolError(fmt.Errorf(
					"Unknown skill: %s. Available skills: %s",
					skill, strings.Join(available, ", "),
				)),
				IsError: true,
			}, nil
		}
	}

	// 检查是否禁用模型调用
	if def.DisableModelInvocation {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf(
				"Skill %s cannot be used with Skill tool due to disable-model-invocation",
				def.Name,
			)),
			IsError: true,
		}, nil
	}

	// 构建技能提示词（替换参数占位符）
	prompt := t.buildPrompt(def, skillArgs)

	// 记录调用
	invocation := SkillInvocation{
		SkillName:    def.Name,
		Args:         skillArgs,
		SkillContent: prompt,
		SkillPath:    def.FilePath,
	}
	t.invocations = append(t.invocations, invocation)

	// 根据执行模式处理
	if def.IsFork() && t.runner != nil {
		return t.executeFork(ctx, def, skillArgs, prompt)
	}

	return t.executeInline(def, prompt)
}

// executeInline 在当前对话中展开技能提示词
func (t *SkillTool) executeInline(def *SkillDefinition, prompt string) (*agent.ToolResult, error) {
	result := &SkillResult{
		Success:     true,
		CommandName: def.Name,
		Status:      "inline",
	}

	if def.Model != "" {
		result.Model = def.Model
	}

	// 内联模式：将技能提示词作为工具结果返回给 LLM
	// LLM 会将其视为指令并继续执行
	return &agent.ToolResult{
		Content: fmt.Sprintf(`Launching skill: %s

--- SKILL INSTRUCTIONS ---
%s
--- END SKILL INSTRUCTIONS ---

Follow the skill instructions above to complete the task.`, def.Name, prompt),
		RichData: result,
	}, nil
}

// executeFork 启动子 Agent 隔离执行技能
func (t *SkillTool) executeFork(ctx context.Context, def *SkillDefinition, args, prompt string) (*agent.ToolResult, error) {
	if t.runner == nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("skill runner not configured for fork mode")),
			IsError: true,
		}, nil
	}

	// 委托给 SkillRunner 执行
	subResult, err := t.runner.RunSkill(ctx, *def, prompt)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("skill execution failed: %w", err)),
			IsError: true,
		}, nil
	}

	// 包装 fork 结果
	result := &SkillResult{
		Success:     !subResult.IsError,
		CommandName: def.Name,
		Status:      "forked",
		Result:      subResult.Content,
	}
	if subResult.RichData != nil {
		if richMap, ok := subResult.RichData.(map[string]interface{}); ok {
			if agentID, ok := richMap["agentId"].(string); ok {
				result.AgentID = agentID
			}
		}
	}

	return &agent.ToolResult{
		Content: fmt.Sprintf(`Skill "%s" completed (forked execution).

Result:
%s`, def.Name, subResult.Content),
		RichData: result,
	}, nil
}

// buildPrompt 构建技能提示词，替换 $ARGUMENTS 等占位符
func (t *SkillTool) buildPrompt(def *SkillDefinition, args string) string {
	prompt := def.Prompt

	// 替换 $ARGUMENTS 占位符
	prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", args)

	// 替换命名的参数（占位符格式：$paramName）
	if len(def.ArgNames) > 0 && args != "" {
		// 简单实现：将 args 按空格分割为位置参数
		argParts := strings.Fields(args)
		for i, name := range def.ArgNames {
			placeholder := "$" + name
			if i < len(argParts) {
				prompt = strings.ReplaceAll(prompt, placeholder, argParts[i])
			}
		}
	}

	// 添加基础目录信息（如果有）
	if def.BaseDir != "" {
		prompt = fmt.Sprintf("Base directory for this skill: %s\n\n%s", def.BaseDir, prompt)
	}

	return prompt
}

// GetSkills 获取所有已加载的技能
func (t *SkillTool) GetSkills() []SkillDefinition {
	return t.skills
}

// RefreshSkills 刷新技能列表（重新从文件系统加载）
func (t *SkillTool) RefreshSkills(builtinSkills []SkillDefinition) {
	if t.loader != nil {
		t.skills = t.loader.LoadSkills(builtinSkills)
	}
}

// GetInvocations 获取技能调用历史
func (t *SkillTool) GetInvocations() []SkillInvocation {
	return t.invocations
}

// FormatSkillsPrompt 格式化技能列表为 LLM 可用的提示词
//
// 参考 Claude Code 的 formatCommandsWithinBudget 设计：
//   - 限制总字符数以控制 token 消耗
//   - 优先保证内置技能的完整描述
//   - 非内置技能在预算不足时截断描述
func FormatSkillsPrompt(skills []SkillDefinition, maxChars int) string {
	visible := ListVisibleSkills(skills)
	if len(visible) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Skills\n\n")
	sb.WriteString("You have the following skills available (use the skill tool to invoke them):\n\n")

	// 计算总长度
	entries := make([]string, 0, len(visible))
	for _, s := range visible {
		desc := s.Description
		if s.WhenToUse != "" {
			desc += " - " + s.WhenToUse
		}
		entry := fmt.Sprintf("- **%s**: %s", s.Name, desc)
		if s.ArgumentHint != "" {
			entry += fmt.Sprintf(" (args: %s)", s.ArgumentHint)
		}
		entries = append(entries, entry)
	}

	totalLen := 0
	for _, e := range entries {
		totalLen += len(e) + 1 // +1 for newline
	}

	if maxChars <= 0 || totalLen <= maxChars {
		sb.WriteString(strings.Join(entries, "\n"))
	} else {
		// 截断模式：内置技能保持完整，其他技能截断描述
		for i, s := range visible {
			if s.IsBuiltIn() {
				sb.WriteString(entries[i])
			} else {
				// 截断描述到剩余预算
				shortDesc := s.Description
				if len(shortDesc) > 80 {
					shortDesc = shortDesc[:77] + "..."
				}
				sb.WriteString(fmt.Sprintf("- **%s**: %s", s.Name, shortDesc))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
