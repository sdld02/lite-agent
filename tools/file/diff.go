package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	// contextLines 显示上下文行数
	contextLines = 3
	// maxFileSize 最大允许 diff 的文件大小 (50MB)
	maxFileSize = 50 << 20
	// maxDiffOutputSize 最大 diff 输出大小 (10MB)
	maxDiffOutputSize = 10 << 20
)

// FileDiffInput 输入参数
type FileDiffInput struct {
	FilePathA   string `json:"file_path_a"`
	FilePathB   string `json:"file_path_b"`
	Format      string `json:"format"`                // "unified", "html", "simple"，默认 "unified"
	ContextLines int   `json:"context_lines"`         // 上下文行数，默认 3
	//MaxFileSize int64  `json:"max_file_size"`         // 自定义最大文件大小（字节）
}

// FileDiffOutput 输出结果
type FileDiffOutput struct {
	FileA        string `json:"file_a"`
	FileB        string `json:"file_b"`
	Identical    bool   `json:"identical"`
	Diff         string `json:"diff"`
	LinesAdded   int    `json:"lines_added"`
	LinesDeleted int    `json:"lines_deleted"`
	Message      string `json:"message"`
	Error        string `json:"error,omitempty"`
}

// FileDiffTool 比较两个文件的差异
func FileDiffTool(input FileDiffInput) (*FileDiffOutput, error) {
	// 1. 参数验证和默认值设置
	if input.Format == "" {
		input.Format = "unified"
	}
	if input.ContextLines <= 0 {
		input.ContextLines = contextLines
	}
	maxSize := maxFileSize
	//if input.MaxFileSize > 0 {
	//	maxSize = int(input.MaxFileSize)
	//}

	// 2. 路径规范化
	pathA, err := expandPath(input.FilePathA)
	if err != nil {
		return nil, fmt.Errorf("invalid file_path_a: %w", err)
	}
	pathB, err := expandPath(input.FilePathB)
	if err != nil {
		return nil, fmt.Errorf("invalid file_path_b: %w", err)
	}

	// 3. 安全检查
	if err := validatePathSafety(pathA); err != nil {
		return nil, err
	}
	if err := validatePathSafety(pathB); err != nil {
		return nil, err
	}

	// 4. 获取文件信息并检查大小
	infoA, err := os.Stat(pathA)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file_path_a: %w", err)
	}
	infoB, err := os.Stat(pathB)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file_path_b: %w", err)
	}

	if infoA.Size() > int64(maxSize) || infoB.Size() > int64(maxSize) {
		return nil, fmt.Errorf("file too large (max %d MB)", maxSize/(1024*1024))
	}

	// 5. 读取文件 A
	bytesA, err := os.ReadFile(pathA)
	if err != nil {
		return nil, fmt.Errorf("failed to read file_path_a: %w", err)
	}
	if isBinaryFile(bytesA) {
		return nil, fmt.Errorf("cannot diff binary file: %s", filepath.Base(pathA))
	}

	// 6. 读取文件 B
	bytesB, err := os.ReadFile(pathB)
	if err != nil {
		return nil, fmt.Errorf("failed to read file_path_b: %w", err)
	}
	if isBinaryFile(bytesB) {
		return nil, fmt.Errorf("cannot diff binary file: %s", filepath.Base(pathB))
	}

	// 7. 检测并转换编码为 UTF-8
	contentA, err := decodeToUTF8(bytesA)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file_path_a: %w", err)
	}
	contentB, err := decodeToUTF8(bytesB)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file_path_b: %w", err)
	}

	contentA = normalizeLineEndings(contentA)
	contentB = normalizeLineEndings(contentB)

	// 8. 相同文件快速返回
	if contentA == contentB {
		return &FileDiffOutput{
			FileA:     pathA,
			FileB:     pathB,
			Identical: true,
			Message:   "Files are identical",
		}, nil
	}

	// 9. 生成行级 diff
	dmp := diffmatchpatch.New()
	
	// 优化 diff 性能
	dmp.DiffTimeout = 2.0 // 2 秒超时
	
	runesA, runesB, lineArray := dmp.DiffLinesToRunes(contentA, contentB)
	diffs := dmp.DiffMainRunes(runesA, runesB, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)
	diffs = dmp.DiffCleanupSemantic(diffs)

	// 10. 根据格式生成输出
	var diffOutput string
	var linesAdded, linesDeleted int
	
	switch input.Format {
	case "html":
		diffOutput = dmp.DiffPrettyHtml(diffs)
		// HTML 格式无法准确统计行数，使用简单统计
		linesAdded, linesDeleted = countDiffLines(diffs)
	case "simple":
		diffOutput, linesAdded, linesDeleted = formatSimpleDiff(diffs)
	default: // "unified"
		diffOutput, linesAdded, linesDeleted = formatUnifiedDiff(diffs, pathA, pathB, input.ContextLines)
	}

	// 11. 限制输出大小
	if len(diffOutput) > maxDiffOutputSize {
		diffOutput = diffOutput[:maxDiffOutputSize] + "\n... (diff truncated due to size limit)"
	}

	return &FileDiffOutput{
		FileA:        pathA,
		FileB:        pathB,
		Identical:    false,
		Diff:         diffOutput,
		LinesAdded:   linesAdded,
		LinesDeleted: linesDeleted,
		Message:      fmt.Sprintf("Files differ: +%d/-%d lines", linesAdded, linesDeleted),
	}, nil
}

// formatUnifiedDiff 格式化为 unified diff 格式
func formatUnifiedDiff(diffs []diffmatchpatch.Diff, pathA, pathB string, contextLines int) (string, int, int) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n+++ %s\n", pathA, pathB))

	linesAdded := 0
	linesDeleted := 0

	for _, d := range diffs {
		// 正确分割行，保留末尾换行信息
		lines := splitLines(d.Text)
		
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			for _, line := range lines {
				if line != "" {
					sb.WriteString("- " + line + "\n")
					linesDeleted++
				}
			}
		case diffmatchpatch.DiffInsert:
			for _, line := range lines {
				if line != "" {
					sb.WriteString("+ " + line + "\n")
					linesAdded++
				}
			}
		case diffmatchpatch.DiffEqual:
			writeContextLines(&sb, lines, contextLines)
		}
	}

	return sb.String(), linesAdded, linesDeleted
}

// formatSimpleDiff 格式化为简单格式（只显示增删行）
func formatSimpleDiff(diffs []diffmatchpatch.Diff) (string, int, int) {
	var sb strings.Builder
	linesAdded := 0
	linesDeleted := 0

	for _, d := range diffs {
		lines := splitLines(d.Text)
		
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			for _, line := range lines {
				if line != "" {
					sb.WriteString(fmt.Sprintf("[-] %s\n", line))
					linesDeleted++
				}
			}
		case diffmatchpatch.DiffInsert:
			for _, line := range lines {
				if line != "" {
					sb.WriteString(fmt.Sprintf("[+] %s\n", line))
					linesAdded++
				}
			}
		}
	}

	return sb.String(), linesAdded, linesDeleted
}

// writeContextLines 写入上下文行，支持可配置的上下文行数
func writeContextLines(sb *strings.Builder, lines []string, contextLines int) {
	if len(lines) == 0 {
		return
	}

	// 过滤掉空行（但保留有意义的空行表示）
	nonEmptyLines := make([]string, 0, len(lines))
	for _, line := range lines {
		nonEmptyLines = append(nonEmptyLines, line)
	}

	if len(nonEmptyLines) <= contextLines*2 {
		// 全部显示
		for _, line := range nonEmptyLines {
			sb.WriteString("  " + line + "\n")
		}
	} else {
		// 显示前 contextLines 行
		for i := 0; i < contextLines; i++ {
			sb.WriteString("  " + nonEmptyLines[i] + "\n")
		}
		
		hidden := len(nonEmptyLines) - contextLines*2
		sb.WriteString(fmt.Sprintf("  ... (%d lines unchanged) ...\n", hidden))
		
		// 显示后 contextLines 行
		start := len(nonEmptyLines) - contextLines
		for i := start; i < len(nonEmptyLines); i++ {
			sb.WriteString("  " + nonEmptyLines[i] + "\n")
		}
	}
}

// splitLines 正确分割行，保留行内容但不包括末尾换行符
func splitLines(text string) []string {
	if text == "" {
		return []string{""}
	}
	
	lines := strings.Split(text, "\n")
	
	// 如果原始文本以换行结尾，Split 会产生最后一个空元素
	if strings.HasSuffix(text, "\n") {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	}
	
	// 确保至少有一行
	if len(lines) == 0 {
		return []string{""}
	}
	
	return lines
}

// countDiffLines 统计 diff 中的行数变化
func countDiffLines(diffs []diffmatchpatch.Diff) (int, int) {
	added := 0
	deleted := 0
	
	for _, d := range diffs {
		lines := splitLines(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			added += len(lines)
		case diffmatchpatch.DiffDelete:
			deleted += len(lines)
		}
	}
	
	return added, deleted
}

// decodeToUTF8 检测并转换文本编码到 UTF-8
func decodeToUTF8(data []byte) (string, error) {
	// 检查是否为有效的 UTF-8
	if utf8.Valid(data) {
		return string(data), nil
	}
	
	// TODO: 可以添加其他编码支持，如 GBK、ISO-8859-1 等
	// 这里简单返回原始字符串，可能会导致乱码
	return string(data), nil
}

// expandPath 展开路径中的 ~ 和变量
func expandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	
	// 展开 ~ 到用户主目录
	if strings.HasPrefix(path, "~/") || path == "~" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		if path == "~" {
			path = homeDir
		} else {
			path = filepath.Join(homeDir, path[2:])
		}
	}
	
	// 展开环境变量
	path = os.ExpandEnv(path)
	
	// 转换为绝对路径
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	
	return absPath, nil
}


// isBinaryFile 检测是否为二进制文件
func isBinaryFile(data []byte) bool {
	// 检查前 512 字节（足够检测大多数文件类型）
	checkLen := 512
	if len(data) < checkLen {
		checkLen = len(data)
	}
	
	// 查找 NULL 字节（二进制文件的典型特征）
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// normalizeLineEndings 统一换行符为 LF
func normalizeLineEndings(content string) string {
	// 将 CRLF 和 CR 统一替换为 LF
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return content
}

// isUNCPath 检测是否为 UNC 路径（Windows 网络路径）
func isUNCPath(path string) bool {
	return strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`)
}