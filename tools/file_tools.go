package tools

import (
	"context"
	"fmt"
	"strings"

	"lite-agent/agent"
	"lite-agent/tools/file"
)

// ==================== FileEdit 工具包装器 ====================

// FileEditToolWrapper 文件编辑工具，包装 file.FileEditTool
type FileEditToolWrapper struct{}

func NewFileEditTool() *FileEditToolWrapper {
	return &FileEditToolWrapper{}
}

func (t *FileEditToolWrapper) Name() string {
	return "file_edit"
}

func (t *FileEditToolWrapper) Description() string {

	return `\n在文件中执行精确的字符串替换。
用法：
- 在进行编辑之前，你必须在本次对话中至少使用一次 file_read 工具读取文件内容。如果在未读取文件的情况下尝试编辑，此工具将会报错。
- 当编辑从 Read 工具输出的文本时，务必保持与行号前缀之后完全一致的缩进（制表符/空格）。行号前缀格式为：${prefixFormat}。其后才是需要匹配的实际文件内容。不要在 old_string 或 new_string 中包含任何行号前缀内容。
- 始终优先编辑代码库中的已有文件。除非明确要求，否则不要创建新文件。
- 仅在用户明确要求时才使用 emoji；除非被要求，否则不要在文件中添加 emoji。
- 如果 old_string 在文件中不是唯一的，编辑将失败。请提供包含更多上下文的更大字符串以确保唯一性，或使用 replace_all 来替换所有匹配项。
- 使用尽可能小但能明确唯一定位的 old_string —— 通常连续 2-4 行就足够。避免在较少内容即可唯一定位目标时提供 10 行以上的上下文。
- 当需要在整个文件中替换或重命名字符串时，请使用 replace_all 参数（例如重命名变量时非常有用）。
- line_start/line_end：按行号范围替换整行，无需提供 old_string，更简单可靠。
- dry_run：设为 true 预览 diff 而不实际写入，确认无误后再去掉此参数执行。
- edits：一次传入多个编辑操作数组 [{"old":"...","new":"..."}, ...]，减少往返。`
}

func (t *FileEditToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要编辑的文件的绝对路径",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "要被替换的原始文本。设为空字符串可追加内容到文件末尾",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "替换后的新文本",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "是否替换所有匹配项，默认只替换第一个",
			},
			"line_start": map[string]interface{}{
				"type":        "integer",
				"description": "替换整行的起始行号（1-based）。配合 line_end 使用，替换指定行号范围。与 old_string 二选一",
			},
			"line_end": map[string]interface{}{
				"type":        "integer",
				"description": "替换整行的结束行号（1-based）。默认等于 line_start（只替换一行）",
			},
			"dry_run": map[string]interface{}{
				"type":        "boolean",
				"description": "预览模式：只返回 diff 不实际写入文件。默认 false",
			},
			"edits": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old": map[string]interface{}{"type": "string", "description": "要被替换的原始文本"},
						"new": map[string]interface{}{"type": "string", "description": "替换后的新文本"},
					},
				},
				"description": "批量编辑操作数组，一次传入多个 {old, new} 对。与 old_string/new_string 互斥",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 修复 main.go 中的空指针异常",
			},
		},
		"required": []string{"file_path", "old_string", "new_string", "intent"},
	}
}

func (t *FileEditToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	filePath, _ := args["file_path"].(string)
	oldString, _ := args["old_string"].(string)
	newString, _ := args["new_string"].(string)
	replaceAll, _ := args["replace_all"].(bool)
	lineStart, _ := args["line_start"].(float64)
	lineEnd, _ := args["line_end"].(float64)
	dryRun, _ := args["dry_run"].(bool)

	// 解析 edits 数组
	var edits []file.EditPair
	if rawEdits, ok := args["edits"].([]interface{}); ok {
		for _, raw := range rawEdits {
			if m, ok := raw.(map[string]interface{}); ok {
				o, _ := m["old"].(string)
				n, _ := m["new"].(string)
				edits = append(edits, file.EditPair{Old: o, New: n})
			}
		}
	}

	if filePath == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("file_path 参数不能为空"),
			IsError: true,
		}, nil
	}

	input := file.FileEditInput{
		FilePath:   filePath,
		OldString:  oldString,
		NewString:  newString,
		ReplaceAll: replaceAll,
		LineStart:  int(lineStart),
		LineEnd:    int(lineEnd),
		DryRun:     dryRun,
		Edits:      edits,
	}

	output, err := file.FileEditTool(input)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(err),
			IsError: true,
		}, nil
	}

	// 构建输出
	var content string
	if output.DryRun {
		content = "[DRY RUN 预览] "
	} else {
		content = ""
	}

	if oldString == "" && len(edits) == 0 && lineStart == 0 {
		content += fmt.Sprintf("New file created at: %s", filePath)
	} else if len(edits) > 0 {
		content += fmt.Sprintf("The file %s has been updated. %d edit(s) applied successfully.", filePath, len(edits))
	} else if lineStart > 0 {
		content += fmt.Sprintf("The file %s has been updated. Lines %d-%d replaced.", filePath, int(lineStart), int(lineEnd))
	} else if replaceAll {
		content += fmt.Sprintf("The file %s has been updated. All occurrences successfully replaced.", filePath)
	} else {
		content += fmt.Sprintf("The file %s has been updated successfully.", filePath)
	}

	if output.Patch != "" {
		content += "\n\n" + output.Patch
	}

	if output.Suggestions != "" {
		content += "\n\n" + output.Suggestions
	}

	return &agent.ToolResult{
		Content:  content,
		RichData: output,
	}, nil
}

// ==================== FileWrite 工具包装器 ====================

// FileWriteToolWrapper 文件写入工具，包装 file.FileWriteTool
type FileWriteToolWrapper struct{}

func NewFileWriteTool() *FileWriteToolWrapper {
	return &FileWriteToolWrapper{}
}

func (t *FileWriteToolWrapper) Name() string {
	return "file_write"
}

func (t *FileWriteToolWrapper) Description() string {
	return `\n将文件写入本地文件系统。
用法：
- 如果提供的路径下已存在文件，此工具会覆盖该文件。如果这是一个已有文件，你必须先使用file_read 工具读取该文件的内容。否则此工具将执行失败
- 修改已有文件时优先使用 Edit 工具 —— 它只会发送差异（diff）。仅在创建新文件或需要完全重写时使用此工具。
- 除非用户明确要求，否则不要创建文档文件（*.md）或 README 文件。
- 仅在用户明确要求时才使用 emoji；除非被要求，否则不要在文件中写入 emoji。`
}

func (t *FileWriteToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要写入的文件的绝对路径",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "要写入的文件内容",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 创建配置文件 config.yaml",
			},
		},
		"required": []string{"file_path", "content", "intent"},
	}
}

func (t *FileWriteToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	filePath, _ := args["file_path"].(string)
	content, _ := args["content"].(string)

	if filePath == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("file_path 参数不能为空"),
			IsError: true,
		}, nil
	}

	input := file.FileWriteInput{
		FilePath: filePath,
		Content:  content,
	}

	output, err := file.FileWriteTool(input)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(err),
			IsError: true,
		}, nil
	}

	// LLM 只看到精简确认文本
	var resultContent string
	if output.Type == "create" {
		resultContent = fmt.Sprintf("File created successfully at: %s", filePath)
	} else {
		resultContent = fmt.Sprintf("The file %s has been updated successfully.", filePath)
	}

	return &agent.ToolResult{
		Content:  resultContent,
		RichData: output,
	}, nil
}

// ==================== FileDiff 工具包装器 ====================

// FileDiffToolWrapper 文件比较工具，包装 file.FileDiffTool
type FileDiffToolWrapper struct{}

func NewFileDiffTool() *FileDiffToolWrapper {
	return &FileDiffToolWrapper{}
}

func (t *FileDiffToolWrapper) Name() string {
	return "file_diff"
}

func (t *FileDiffToolWrapper) Description() string {
	return "比较两个文件的差异。返回行级别的 diff 结果，包括新增行数和删除行数。"
}

func (t *FileDiffToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path_a": map[string]interface{}{
				"type":        "string",
				"description": "第一个文件的路径（基准文件）",
			},
			"file_path_b": map[string]interface{}{
				"type":        "string",
				"description": "第二个文件的路径（比较文件）",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "比较结果的格式，可选值有：unified, html, simple。默认 unified",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 对比修改前后的差异",
			},
		},
		"required": []string{"file_path_a", "file_path_b", "intent"},
	}
}

func (t *FileDiffToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	filePathA, _ := args["file_path_a"].(string)
	filePathB, _ := args["file_path_b"].(string)

	if filePathA == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("file_path_a 参数不能为空"),
			IsError: true,
		}, nil
	}
	if filePathB == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("file_path_b 参数不能为空"),
			IsError: true,
		}, nil
	}

	input := file.FileDiffInput{
		FilePathA: filePathA,
		FilePathB: filePathB,
	}

	output, err := file.FileDiffTool(input)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(err),
			IsError: true,
		}, nil
	}

	// LLM 需要看到 diff 内容来做代码分析
	var resultContent string
	if output.Identical {
		resultContent = "Files are identical."
	} else {
		resultContent = output.Diff
	}

	return &agent.ToolResult{
		Content:  resultContent,
		RichData: output,
	}, nil
}

// ==================== FileRead 工具包装器 ====================

// FileReadToolWrapper 文件读取工具，包装 file.FileReadTool
type FileReadToolWrapper struct{}

func NewFileReadTool() *FileReadToolWrapper {
	return &FileReadToolWrapper{}
}

func (t *FileReadToolWrapper) Name() string {
	return "file_read"
}

func (t *FileReadToolWrapper) Description() string {
	return `读取文件内容，支持分页和多种读取模式。

用法：
- 读取指定路径的文件内容。
- 支持 offset 参数从指定行开始读取（1-based），搭配 max_lines 实现分页。
- head_lines=N 快捷读取前N行；tail_lines=N 快捷读取后N行。
- 默认不显示行号（避免干扰 file_edit）。如需要行号，设置 show_line_numbers=true。
- 截断时会给出智能提示：包含文件大小、总行数、建议分段策略等信息。
- 返回的元信息头包含 [File: name | N lines | X.XKB]，帮助快速了解文件全貌。
- 支持相对路径和绝对路径。文件不存在或为目录时返回对应状态提示。`
}

func (t *FileReadToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "要读取的文件的绝对路径",
			},
			"max_lines": map[string]interface{}{
				"type":        "integer",
				"description": "最大读取行数，0表示无限制（默认1000）",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "从第几行开始读取（1-based），默认从第1行开始。用于大文件分页读取",
			},
			"head_lines": map[string]interface{}{
				"type":        "integer",
				"description": "只读取文件前N行（与offset/tail_lines互斥）",
			},
			"tail_lines": map[string]interface{}{
				"type":        "integer",
				"description": "只读取文件后N行（与offset/head_lines互斥）",
			},
			"show_line_numbers": map[string]interface{}{
				"type":        "boolean",
				"description": "是否在输出中显示行号，默认false。行号格式为 '行号\\t内容'",
			},
			"encoding": map[string]interface{}{
				"type":        "string",
				"description": "文件编码，默认utf-8。支持: utf-8, gbk, latin-1, utf-16le, utf-16be",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如: 查看 main.go 的内容以理解程序入口",
			},
		},
		"required": []string{"file_path", "intent"},
	}
}

func (t *FileReadToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	filePath, _ := args["file_path"].(string)
	maxLines, _ := args["max_lines"].(float64)
	offset, _ := args["offset"].(float64)
	headLines, _ := args["head_lines"].(float64)
	tailLines, _ := args["tail_lines"].(float64)
	showLineNumbers, hasShowLN := args["show_line_numbers"].(bool)
	encoding, _ := args["encoding"].(string)

	if filePath == "" {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("file_path 参数不能为空"),
			IsError: true,
		}, nil
	}

	var showLN *bool
	if hasShowLN {
		showLN = &showLineNumbers
	}

	input := file.FileReadInput{
		FilePath:        filePath,
		MaxLines:        int(maxLines),
		Offset:          int(offset),
		HeadLines:       int(headLines),
		TailLines:       int(tailLines),
		ShowLineNumbers: showLN,
		Encoding:        encoding,
	}

	output, err := file.FileReadTool(input)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(err),
			IsError: true,
		}, nil
	}

	// 文件不存在
	if !output.Exists {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("file not found: %s", filePath)),
			IsError: true,
		}, nil
	}

	// 是目录
	if output.IsDirectory {
		return &agent.ToolResult{
			Content: fmt.Sprintf("%s is a directory, not a file.", filePath),
			IsError: true,
		}, nil
	}

	// 成功：根据 show_line_numbers 决定格式
	var content string

	// 先输出元信息头
	if output.Header != "" {
		content = output.Header + "\n\n"
	}

	if hasShowLN && showLineNumbers {
		content += formatContentWithLineNumbers(output.Content, output.Truncated, output.Lines, output.LinesRead, output.LineStart)
	} else {
		content += output.Content
		if output.Truncated {
			content += fmt.Sprintf("\n\n[截断: 显示第 %d-%d 行 / 共 %d 行]", output.LineStart, output.LineStart+output.LinesRead-1, output.Lines)
		}
	}

	return &agent.ToolResult{
		Content:  content,
		RichData: output,
	}, nil
}

// formatContentWithLineNumbers 将文件内容格式化为带行号的纯文本
func formatContentWithLineNumbers(content string, truncated bool, totalLines, linesRead, lineStart int) string {
	lines := strings.Split(content, "\n")
	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%4d\t%s\n", lineStart+i, line))
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("\n... (truncated, showing lines %d-%d/%d)\n", lineStart, lineStart+linesRead-1, totalLines))
	}
	return sb.String()
}
