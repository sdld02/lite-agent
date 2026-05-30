package skill

// BuiltinSkills 内置技能列表
//
// 参考 Claude Code 的内置斜杠命令（/commit、/review-pr 等）设计。
// 内置技能在代码中硬编码，具有最高优先级——如果用户定义了同名技能，
// 内置版本会被使用（确保核心功能不被覆盖）。
var BuiltinSkills = []SkillDefinition{
	GitCommitSkill,
	ReviewPRSkill,
	ExplainCodeSkill,
	PlanModeSkill,
}

// GitCommitSkill 生成 Git commit message
//
// 对应 Claude Code 的 /commit 命令。
// inline 模式：分析 git diff 结果，生成规范的 commit message。
var GitCommitSkill = SkillDefinition{
	Name:        "commit",
	Description: "Generate a well-formatted git commit message from staged changes",
	WhenToUse:   "When the user wants to commit changes, generate a commit message, or says something like 'commit this' or '/commit'",
	Source:      SourceBuiltIn,
	Context:     ContextInline,
	ArgumentHint: "[message prefix or additional instructions]",
	ArgNames:    []string{"hint"},
	ProgressMessage: "Analyzing staged changes and generating commit message...",
	Prompt: `You are an expert at writing clear, concise git commit messages.

Your task is to analyze the git diff output and generate an appropriate commit message following the Conventional Commits specification.

Guidelines:
1. Use the format: <type>(<scope>): <description>
2. Types: feat, fix, docs, style, refactor, perf, test, chore, ci, build
3. Keep the first line under 72 characters
4. Add a blank line after the summary, then bullet points for details if needed
5. Use imperative mood ("add" not "added" or "adds")
6. Do not end the summary with a period

First, run "git diff --cached" (or just "git diff" if nothing is staged) to see the changes.
Then generate the commit message.

If the user provided a hint (in $ARGUMENTS), use it to guide the message style.`,
}

// ReviewPRSkill 审查 Pull Request 变更
//
// 对应 Claude Code 的 /review-pr 命令。
// inline 模式：对比 PR 变更，给出代码审查意见。
var ReviewPRSkill = SkillDefinition{
	Name:        "review-pr",
	Description: "Review pull request changes and provide actionable feedback",
	WhenToUse:   "When the user wants to review a PR, says '/review-pr', or asks for code review on a branch or set of changes",
	Source:      SourceBuiltIn,
	Context:     ContextInline,
	ArgumentHint: "<PR_NUMBER or BRANCH_NAME>",
	ArgNames:    []string{"target"},
	ProgressMessage: "Reviewing changes...",
	Prompt: `You are a senior code reviewer. Your task is to review code changes and provide constructive, actionable feedback.

Review process:
1. First, use git to check out or diff against the target branch/PR mentioned in $ARGUMENTS
2. Analyze the changes for:
   - Logic errors and potential bugs
   - Security vulnerabilities
   - Performance issues
   - Code style and consistency
   - Test coverage gaps
   - Documentation needs

Output format:
- Start with a brief summary (2-3 sentences)
- List findings by severity: 🔴 Critical, 🟡 Important, 🔵 Suggestion
- For each finding, explain the issue and suggest a specific fix
- End with an overall assessment (approve / request changes / comment)

Be constructive, not harsh. Focus on the code, not the person.`,
}

// ExplainCodeSkill 解释代码片段
//
// inline 模式：对选中的代码或文件进行详细解释。
var ExplainCodeSkill = SkillDefinition{
	Name:        "explain-code",
	Description: "Explain how a piece of code works in detail",
	WhenToUse:   "When the user asks to explain code, says '/explain', or wants to understand how something works",
	Source:      SourceBuiltIn,
	Context:     ContextInline,
	ArgumentHint: "[file path or function name]",
	ArgNames:    []string{"target"},
	ProgressMessage: "Analyzing code...",
	Prompt: `You are an expert programming mentor. Your task is to explain how a piece of code works in clear, educational terms.

Instructions:
1. Read the file or code section referenced in $ARGUMENTS (if provided)
2. Provide a structured explanation:
   - **Overview**: What does this code do at a high level? (1-2 sentences)
   - **Key Components**: Break down the main parts (functions, classes, modules)
   - **Data Flow**: How does data move through the code?
   - **Important Patterns**: Any design patterns, algorithms, or techniques used
   - **Edge Cases**: Are there any notable edge cases or gotchas?
3. Use analogies if helpful
4. Mention the language/framework features being used

Adapt the level of detail to the complexity of the code.`,
}

// PlanModeSkill 进入规划模式
//
// fork 模式：启动一个只读的规划子 Agent，分析任务并制定计划。
var PlanModeSkill = SkillDefinition{
	Name:        "plan",
	Description: "Enter planning mode to analyze a task and create a detailed plan before execution",
	WhenToUse:   "When the user wants to plan before coding, says '/plan', or asks for a detailed plan for a complex task",
	Source:      SourceBuiltIn,
	Context:     ContextFork,
	AgentType:   "Plan",
	Tools:       []string{"file_read", "code_probe", "code_stats", "lsp", "shell", "task_create", "task_list"},
	MaxTurns:    30,
	ArgumentHint: "<task description>",
	ArgNames:    []string{"task"},
	ProgressMessage: "Creating a plan...",
	Prompt: `You are a planning expert. Your task is to analyze a task and create a detailed, actionable plan.

The task to plan for is: $ARGUMENTS

Instructions:
1. First, explore the codebase to understand the relevant context:
   - Use code_probe to understand project structure
   - Use file_read to examine relevant files
   - Use lsp to find definitions and references

2. Then create a detailed plan with:
   - **Goal**: Clear statement of what needs to be achieved
   - **Investigation**: What you explored and key findings
   - **Step-by-step Plan**: Numbered list of implementation steps
   - **Files to Modify**: List each file and what changes are needed
   - **Testing Strategy**: How to verify the changes work
   - **Risk Assessment**: Potential issues and mitigations

3. Create tasks using task_create for each major step

DO NOT modify any files. This is a READ-ONLY planning exercise. Your output will be reviewed before any implementation begins.`,
}
