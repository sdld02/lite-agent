package tools

import (
	"context"
	"fmt"
	"os"

	"lite-agent/agent"
	"lite-agent/tools/lsp"
)

// LSPToolWrapper 将 LSP 代码智能功能包装为 Agent 工具。
//
// 支持 9 种 LSP 操作：
//   - goToDefinition / findReferences / hover / documentSymbol
//   - workspaceSymbol / goToImplementation
//   - prepareCallHierarchy / incomingCalls / outgoingCalls
//
// 多语言支持通过扩展名路由到对应的 LSP 服务器：
//
//	.ts/.tsx → typescript-language-server
//	.go      → gopls
//	.py      → pyright
//	.rs      → rust-analyzer
type LSPToolWrapper struct{}

// NewLSPTool 创建 LSP 工具
func NewLSPTool() *LSPToolWrapper {
	return &LSPToolWrapper{}
}

func (t *LSPToolWrapper) Name() string {
	return "lsp"
}

func (t *LSPToolWrapper) Description() string {
	return `与 Language Server Protocol (LSP) 服务器交互，获取代码智能信息。

支持的操作:
- goToDefinition: 查找符号的定义位置
- findReferences: 查找符号的所有引用
- hover: 获取符号的悬停信息（文档、类型信息）
- documentSymbol: 获取文档中的所有符号（函数、类、变量等）
- workspaceSymbol: 搜索整个工作区的符号
- goToImplementation: 查找接口或抽象方法的实现
- prepareCallHierarchy: 在指定位置获取调用层次项
- incomingCalls: 查找所有调用此函数的方法
- outgoingCalls: 查找此函数调用的所有方法

所有操作需要:
- filePath: 要操作的文件路径
- line: 行号（1-based，与编辑器显示一致）
- character: 字符偏移（1-based，与编辑器显示一致）

注意: LSP 服务器必须已安装并配置。如果当前文件类型没有可用的服务器，将返回错误。`
}

func (t *LSPToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"goToDefinition",
					"findReferences",
					"hover",
					"documentSymbol",
					"workspaceSymbol",
					"goToImplementation",
					"prepareCallHierarchy",
					"incomingCalls",
					"outgoingCalls",
				},
				"description": "要执行的 LSP 操作",
			},
			"filePath": map[string]interface{}{
				"type":        "string",
				"description": "要操作的文件路径（绝对路径或相对路径）",
			},
			"line": map[string]interface{}{
				"type":        "integer",
				"description": "行号（1-based，与编辑器显示一致）",
			},
			"character": map[string]interface{}{
				"type":        "integer",
				"description": "字符偏移（1-based，与编辑器显示一致）",
			},
		},
		"required": []string{"operation", "filePath", "line", "character"},
	}
}

func (t *LSPToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	operation, _ := args["operation"].(string)
	filePath, _ := args["filePath"].(string)
	line, _ := args["line"].(float64)
	character, _ := args["character"].(float64)

	if operation == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("operation 参数不能为空"), IsError: true}, nil
	}
	if filePath == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("filePath 参数不能为空"), IsError: true}, nil
	}

	// 确保全局管理器已初始化（惰性初始化）
	ensureManagerInitialized()

	// 检查 LSP 是否可用
	if !lsp.IsAvailable() {
		return &agent.ToolResult{
			Content: fmt.Sprintf("LSP 不可用: 没有配置 LSP 服务器。\n"+
				"请确保已安装对应的 LSP 服务器:\n"+
				"  Go:          go install golang.org/x/tools/gopls@latest\n"+
				"  TypeScript:  npm install -g typescript-language-server typescript\n"+
				"  Python:      pip install pyright\n"+
				"  Rust:        rustup component add rust-analyzer\n"),
			IsError: true,
		}, nil
	}

	input := lsp.LSPToolInput{
		Operation: lsp.Operation(operation),
		FilePath:  filePath,
		Line:      int(line),
		Character: int(character),
	}

	output, err := lsp.ExecuteLSPOperation(input)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(err), IsError: true}, nil
	}

	// 格式化为可读输出
	var content string
	if output.ResultCount > 0 && output.FileCount > 0 {
		content = fmt.Sprintf("[LSP %s] %s\n\n操作: %s | 文件: %s | 结果: %d 条 | 涉及: %d 个文件",
			output.Operation, output.Result, output.Operation, output.FilePath,
			output.ResultCount, output.FileCount)
	} else {
		content = fmt.Sprintf("[LSP %s] %s", output.Operation, output.Result)
	}

	return &agent.ToolResult{Content: content}, nil
}

// ensureManagerInitialized 惰性初始化全局 LSP 管理器
func ensureManagerInitialized() {
	if lsp.GetGlobalManager() != nil {
		return
	}
	workDir, err := os.Getwd()
	if err != nil {
		workDir = "."
	}
	lsp.InitGlobalManager(workDir)
}
