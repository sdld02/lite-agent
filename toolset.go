package main

import (
	"fmt"

	"lite-agent/agent"
	"lite-agent/server"
	"lite-agent/tools"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools/skill"
)

// ============================================================================
// 工具装配的单一数据源
//
// 说明：内置工具的「注册 / 装配 / 每连接工厂 / 启动横幅展示」原先分散在
// main.go 的多处硬编码列表中，增删工具极易遗漏。此处集中为唯一来源：
//   - baseToolSpecs  -> 注册表、主 Agent、Server 每连接工具工厂共用
//   - toolBannerLines -> 三种模式启动横幅共用
//   - agent / skill / mcp 三个特殊工具因行为差异，仍显式处理
// ============================================================================

// toolSpec 描述一个内置工具的构造规格（工厂）。
type toolSpec struct {
	name    string
	factory func() agent.Tool
}

// baseToolSpecs 返回内置工具的构造规格（不含 agent / skill / mcp 三个特殊工具）。
// providerCfg 供 web_fetch 等需要 LLM 的工具使用。
func baseToolSpecs(providerCfg agent.LLMProvider) []toolSpec {
	return []toolSpec{
		{"calculator", func() agent.Tool { return tools.NewCalculatorTool() }},
		{"system_info", func() agent.Tool { return tools.NewSystemInfoTool() }},
		{"shell", func() agent.Tool { return tools.NewShellToolUnsafe() }},
		{"file_edit", func() agent.Tool { return tools.NewFileEditTool() }},
		{"file_write", func() agent.Tool { return tools.NewFileWriteTool() }},
		{"file_diff", func() agent.Tool { return tools.NewFileDiffTool() }},
		{"file_read", func() agent.Tool { return tools.NewFileReadTool() }},
		{"code_probe", func() agent.Tool { return tools.NewCodeProbeTool() }},
		{"code_stats", func() agent.Tool { return tools.NewCodeStatsTool() }},
		{"lsp", func() agent.Tool { return tools.NewLSPTool() }},
		{"task_create", func() agent.Tool { return tools.NewTaskCreateTool() }},
		{"task_update", func() agent.Tool { return tools.NewTaskUpdateTool() }},
		{"task_list", func() agent.Tool { return tools.NewTaskListTool() }},
		{"task_get", func() agent.Tool { return tools.NewTaskGetTool() }},
		{"ask_user_question", func() agent.Tool { return tools.NewAskUserQuestionTool() }},
		{"grep", func() agent.Tool { return tools.NewGrepTool() }},
		{"glob", func() agent.Tool { return tools.NewGlobTool() }},
		{"web_fetch", func() agent.Tool { return tools.NewWebFetchTool(providerCfg) }},
		{"web_search", func() agent.Tool { return tools.NewWebSearchTool() }},
	}
}

// registerBaseTools 将基础工具注册到子 Agent 工具注册表。
func registerBaseTools(reg *agentpkg.ToolRegistry, specs []toolSpec) {
	for _, s := range specs {
		reg.Register(s.name, s.factory)
	}
}

// addBaseTools 将基础工具加入主 Agent。
func addBaseTools(ag *agent.Agent, specs []toolSpec) {
	for _, s := range specs {
		ag.AddTool(s.factory())
	}
}

// baseToolFactories 将基础工具转换为 Server 的每连接工具工厂。
func baseToolFactories(specs []toolSpec) []server.ToolFactory {
	factories := make([]server.ToolFactory, 0, len(specs))
	for _, s := range specs {
		factories = append(factories, s.factory)
	}
	return factories
}

// toolBannerLines 启动横幅中展示的工具清单（单一来源，三种模式共用）。
// 保留手工对齐的展示格式；task_* 在展示上合并为一行。
var toolBannerLines = []string{
	"  - calculator   : 数学计算",
	"  - system_info  : 系统信息",
	"  - shell        : Shell 命令执行",
	"  - file_edit    : 文件编辑",
	"  - file_write   : 文件写入",
	"  - file_diff    : 文件比较",
	"  - file_read    : 文件读取",
	"  - code_probe   : 项目结构探查",
	"  - code_stats   : 代码行数统计",
	"  - lsp          : LSP 代码智能",
	"  - agent        : 子Agent系统 (general-purpose/Explore/Plan)",
	"  - task_*       : 任务管理 (create/update/list/get)",
	"  - skill        : 技能系统 (commit/review-pr/explain-code/plan)",
	"  - ask_user_question : 用户提问（执行中向用户发起多选题）",
	"  - grep              : 代码搜索（纯Go，正则/glob/三种输出模式）",
	"  - glob              : 文件名匹配（纯Go，支持 ** 递归）",
	"  - web_fetch         : 网页抓取与分析",
	"  - web_search        : DuckDuckGo 网页搜索",
}

// printToolBanner 打印“已加载工具”清单（含条件性的 MCP 行）。
// showSkillCount 为 true 时额外打印内置技能数量（Telegram 模式保持不打印以维持原输出）。
func printToolBanner(showSkillCount bool) {
	fmt.Println("已加载工具:")
	for _, line := range toolBannerLines {
		fmt.Println(line)
	}
	if mgr := tools.GetMCPManager(); mgr != nil && mgr.HasServers() {
		fmt.Println("  - mcp               : MCP 协议工具 (按需加载)")
	}
	if showSkillCount {
		fmt.Printf("  📂 内置技能: %d 个\n", len(skill.BuiltinSkills))
	}
}
