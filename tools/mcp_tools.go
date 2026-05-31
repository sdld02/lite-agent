package tools

import (
	"context"
	"fmt"
	"strings"

	"lite-agent/agent"
	"lite-agent/mcp"
)

// MCPToolWrapper 将 MCP 管理器包装为 agent.Tool 接口。
//
// 设计理念与 Skill 系统一致：
//   - 单一工具入口（"mcp"），通过参数路由到不同服务器和工具
//   - 系统提示词动态展示可用的 MCP 服务器
//   - 按需连接：首次调用服务器时才启动子进程
//
// LLM 使用流程：
//  1. 查看系统提示词中的可用 MCP 服务器列表
//  2. 可选：调用 mcp(operation="list_tools", server="xxx") 发现工具
//  3. 调用 mcp(operation="call_tool", server="xxx", tool="yyy", arguments={...})
type MCPToolWrapper struct {
	manager *mcp.Manager
}

// NewMCPTool 创建 MCP 工具实例
func NewMCPTool(manager *mcp.Manager) *MCPToolWrapper {
	return &MCPToolWrapper{manager: manager}
}

// Name 工具名称
func (t *MCPToolWrapper) Name() string {
	return "mcp"
}

// Description 工具描述
func (t *MCPToolWrapper) Description() string {
	return `调用 MCP (Model Context Protocol) 服务器提供的工具。

MCP 是一种标准协议，让 AI Agent 能够使用外部工具服务（如文件系统访问、GitHub API、数据库等）。

使用方式：
- 先用 operation="list_tools" 查看某个服务器提供了哪些工具
- 再用 operation="call_tool" 调用具体工具，传入 tool 名称和 arguments

注意：
- 首次使用某个 MCP 服务器时会自动启动，可能略有延迟
- 如果服务器连接失败，会返回错误，不影响其他操作
- MCP 工具返回的结果可能是文本、图片或资源链接`
}

// Parameters 工具参数定义
func (t *MCPToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"server": map[string]interface{}{
				"type":        "string",
				"description": "MCP 服务器名称，如 filesystem, github",
			},
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "操作类型：list_tools（列出该服务器的所有工具），call_tool（调用具体工具）",
				"enum":        []string{"list_tools", "call_tool"},
			},
			"tool": map[string]interface{}{
				"type":        "string",
				"description": "要调用的工具名称（operation=call_tool 时需要）",
			},
			"arguments": map[string]interface{}{
				"type":        "object",
				"description": "传递给 MCP 工具的参数（operation=call_tool 时需要），JSON 对象格式",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 通过 GitHub MCP 服务器查询仓库 Issues",
			},
		},
		"required": []string{"server", "operation", "intent"},
	}
}

// Execute 执行 MCP 工具调用
func (t *MCPToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 确保全局管理器可用（惰性初始化）
	if t.manager == nil {
		t.manager = mcp.GetGlobalManager()
		if t.manager == nil {
			return &agent.ToolResult{
				Content: agent.FormatToolError(fmt.Errorf("MCP 管理器未初始化，请检查 mcp.json 配置")),
				IsError: true,
			}, nil
		}
	}

	server, _ := args["server"].(string)
	if server == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("server 参数不能为空"),
			IsError: true,
		}, nil
	}

	operation, _ := args["operation"].(string)
	if operation == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("operation 参数不能为空"),
			IsError: true,
		}, nil
	}

	// 检查服务器是否已配置
	if !t.manager.IsConfigured(server) {
		available := t.manager.ListServers()
		names := make([]string, 0, len(available))
		for name := range available {
			names = append(names, name)
		}
		return &agent.ToolResult{
			Content: agent.FormatToolError(
				fmt.Errorf("未知的 MCP 服务器: %s。可用服务器: %s", server, strings.Join(names, ", ")),
			),
			IsError: true,
		}, nil
	}

	switch operation {
	case "list_tools":
		return t.executeListTools(server)
	case "call_tool":
		toolName, _ := args["tool"].(string)
		if toolName == "" {
			return &agent.ToolResult{
				Content: agent.FormatValidationError("tool 参数不能为空（operation=call_tool 时需要指定工具名称）"),
				IsError: true,
			}, nil
		}
		toolArgs, _ := args["arguments"].(map[string]interface{})
		if toolArgs == nil {
			toolArgs = make(map[string]interface{})
		}
		return t.executeCallTool(ctx, server, toolName, toolArgs)
	default:
		return &agent.ToolResult{
			Content: agent.FormatValidationError("operation 必须是 list_tools 或 call_tool"),
			IsError: true,
		}, nil
	}
}

// executeListTools 列出服务器提供的工具
func (t *MCPToolWrapper) executeListTools(serverName string) (*agent.ToolResult, error) {
	tools, err := t.manager.ListTools(serverName)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("获取 %s 工具列表失败: %w", serverName, err)),
			IsError: true,
		}, nil
	}

	if len(tools) == 0 {
		return &agent.ToolResult{
			Content: fmt.Sprintf("MCP 服务器 %s 没有提供任何工具。", serverName),
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("MCP 服务器 %s 提供以下 %d 个工具：\n\n", serverName, len(tools)))
	for i, tool := range tools {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, tool.Name))
		sb.WriteString(fmt.Sprintf("   %s\n", tool.Description))
		if tool.InputSchema != nil {
			sb.WriteString("   参数: (见 JSON Schema)\n")
		}
	}

	return &agent.ToolResult{
		Content:  sb.String(),
		RichData: tools,
	}, nil
}

// executeCallTool 调用具体工具
func (t *MCPToolWrapper) executeCallTool(ctx context.Context, serverName, toolName string, arguments map[string]interface{}) (*agent.ToolResult, error) {
	result, err := t.manager.CallTool(ctx, serverName, toolName, arguments)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("调用 %s/%s 失败: %w", serverName, toolName, err)),
			IsError: true,
		}, nil
	}

	contentText := result.ContentText()

	if result.IsError {
		return &agent.ToolResult{
			Content: fmt.Sprintf("MCP 工具 %s/%s 返回错误:\n%s", serverName, toolName, contentText),
			IsError: true,
		}, nil
	}

	return &agent.ToolResult{
		Content:  contentText,
		RichData: result,
	}, nil
}

// ---------------------------------------------------------------------------
// 便捷函数（供 main.go 等调用方使用）
// ---------------------------------------------------------------------------

// InitMCPManager 初始化全局 MCP 管理器
//
// 从 homeDir 和 projectRoot 加载 mcp.json 配置，初始化全局管理器。
// 返回是否成功加载了任何配置。
func InitMCPManager(homeDir, projectRoot string) bool {
	configs := mcp.LoadConfig(homeDir, projectRoot)
	if len(configs) == 0 {
		return false
	}

	mcp.InitGlobalManager(configs)
	return true
}

// GetMCPManager 获取全局 MCP 管理器
func GetMCPManager() *mcp.Manager {
	return mcp.GetGlobalManager()
}

// FormatMCPServersPrompt 生成 MCP 服务器列表的系统提示词
//
// 参考 skill.FormatSkillsPrompt 的设计：
//   - 只有存在配置时才生成内容
//   - 限制描述长度避免提示词膨胀
func FormatMCPServersPrompt(manager *mcp.Manager) string {
	if manager == nil || !manager.HasServers() {
		return ""
	}

	descs := manager.GetServerDescriptions()

	var sb strings.Builder
	sb.WriteString("\n## 可用 MCP 服务器\n\n")
	sb.WriteString("你有以下 MCP 服务器可用（使用 `mcp` 工具调用）：\n\n")
	sb.WriteString("使用方式：\n")
	sb.WriteString("- `mcp` + operation=\"list_tools\" 查看服务器提供的工具\n")
	sb.WriteString("- `mcp` + operation=\"call_tool\" 调用具体工具\n\n")

	for _, desc := range descs {
		sb.WriteString(fmt.Sprintf("- **%s**: 命令 `%s`\n", desc.Name, desc.Command))
	}

	return sb.String()
}
