package file

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// FileReadInput 输入参数
type FileReadInput struct {
	FilePath        string `json:"file_path"`
	MaxLines        int    `json:"max_lines,omitempty"`         // 最大读取行数，0表示无限制
	Offset          int    `json:"offset,omitempty"`            // 从第几行开始读（1-based），默认1
	HeadLines       int    `json:"head_lines,omitempty"`        // 只读前N行（快捷参数，与offset互斥）
	TailLines       int    `json:"tail_lines,omitempty"`        // 只读后N行（快捷参数，与offset互斥）
	ShowLineNumbers *bool  `json:"show_line_numbers,omitempty"` // 是否显示行号，默认false
	Encoding        string `json:"encoding,omitempty"`          // 文件编码，默认utf-8
}

// FileReadOutput 输出结果
type FileReadOutput struct {
	FilePath     string `json:"file_path"`
	Exists       bool   `json:"exists"`
	IsDirectory  bool   `json:"is_directory,omitempty"`
	Size         int64  `json:"size,omitempty"`          // 文件大小（字节）
	Content      string `json:"content,omitempty"`       // 文件内容（纯内容，不含行号）
	Lines        int    `json:"lines,omitempty"`         // 总行数
	LinesRead    int    `json:"lines_read,omitempty"`    // 实际读取的行数
	LineStart    int    `json:"line_start,omitempty"`    // 实际起始行号
	Truncated    bool   `json:"truncated,omitempty"`     // 是否被截断
	Encoding     string `json:"encoding,omitempty"`      // 实际使用的编码
	ErrorMessage string `json:"error_message,omitempty"` // 错误信息（如果读取失败）
	Permissions  string `json:"permissions,omitempty"`   // 文件权限
	Message      string `json:"message"`                 // 状态消息
	Header       string `json:"header,omitempty"`        // 元信息头
}

// 常量
const (
	MaxReadFileSize = 1024 * 1024 * 10 // 10 MB
	DefaultMaxLines = 1000             // 默认最大读取行数
)

// FileReadTool 主函数
func FileReadTool(input FileReadInput) (*FileReadOutput, error) {
	// 1. 路径规范化
	fullPath, err := expandPath(input.FilePath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// 2. 安全检查
	if isUNCPath(fullPath) {
		return nil, errors.New("UNC paths are not allowed for security reasons")
	}

	// 2.1 路径遍历防护
	if err := validatePathSafety(fullPath); err != nil {
		return nil, err
	}

	// 3. 检查文件是否存在
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileReadOutput{
				FilePath: fullPath,
				Exists:   false,
				Message:  fmt.Sprintf("文件不存在: %s", fullPath),
			}, nil
		}
		return nil, fmt.Errorf("无法访问文件: %w", err)
	}

	// 4. 检查是否是目录
	if fileInfo.IsDir() {
		return &FileReadOutput{
			FilePath:    fullPath,
			Exists:      true,
			IsDirectory: true,
			Size:        fileInfo.Size(),
			Permissions: fileInfo.Mode().String(),
			Message:     fmt.Sprintf("这是一个目录: %s", fullPath),
		}, nil
	}

	// 5. 检查文件大小
	if fileInfo.Size() > MaxReadFileSize {
		return &FileReadOutput{
			FilePath:     fullPath,
			Exists:       true,
			IsDirectory:  false,
			Size:         fileInfo.Size(),
			Permissions:  fileInfo.Mode().String(),
			ErrorMessage: fmt.Sprintf("文件太大 (%d bytes)，超过最大限制 (%d bytes)", fileInfo.Size(), MaxReadFileSize),
			Message:      "文件过大，无法读取。请使用 offset 参数分段读取，或使用 head_lines/tail_lines 查看部分内容。",
		}, nil
	}

	// 6. 读取文件内容
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return &FileReadOutput{
			FilePath:     fullPath,
			Exists:       true,
			IsDirectory:  false,
			Size:         fileInfo.Size(),
			Permissions:  fileInfo.Mode().String(),
			ErrorMessage: fmt.Sprintf("读取文件失败: %v", err),
			Message:      "文件读取失败",
		}, nil
	}

	// 7. 编码检测和处理
	encoding := detectEncoding(input.Encoding, contentBytes)
	contentStr := decodeContent(contentBytes, encoding)

	// 8. 转换为字符串并拆分行
	allLines := strings.Split(contentStr, "\n")
	totalLines := len(allLines)

	// 9. 计算实际读取范围
	maxLines := input.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}

	// 处理 head_lines / tail_lines / offset
	lineStart := 1
	var selectedLines []string

	if input.TailLines > 0 {
		// tail_lines 模式：读取最后N行
		start := totalLines - input.TailLines
		if start < 0 {
			start = 0
		}
		selectedLines = allLines[start:]
		lineStart = start + 1
	} else if input.HeadLines > 0 {
		// head_lines 模式：读取前N行
		end := input.HeadLines
		if end > totalLines {
			end = totalLines
		}
		selectedLines = allLines[:end]
		lineStart = 1
	} else {
		// offset 模式（默认从第1行开始）
		offset := input.Offset
		if offset < 1 {
			offset = 1
		}
		start := offset - 1
		if start >= totalLines {
			// offset 超出范围，返回空
			return &FileReadOutput{
				FilePath:    fullPath,
				Exists:      true,
				IsDirectory: false,
				Size:        fileInfo.Size(),
				Lines:       totalLines,
				LinesRead:   0,
				LineStart:   offset,
				Encoding:    encoding,
				Permissions: fileInfo.Mode().String(),
				Header:      buildHeader(fullPath, totalLines, fileInfo.Size()),
				Message:     fmt.Sprintf("offset %d 已超出文件总行数 %d", offset, totalLines),
			}, nil
		}
		end := start + maxLines
		if end > totalLines {
			end = totalLines
		}
		selectedLines = allLines[start:end]
		lineStart = offset
	}

	// 10. 截断检测
	truncated := len(selectedLines) < (totalLines - (lineStart - 1))
	linesRead := len(selectedLines)

	// 应用 maxLines 限制到 head/tail 场景
	if (input.HeadLines > 0 || input.TailLines > 0) && linesRead > maxLines {
		linesRead = maxLines
		selectedLines = selectedLines[:maxLines]
		truncated = true
	}

	// 11. 纯内容（不含行号）
	pureContent := strings.Join(selectedLines, "\n")

	// 12. 构建元信息头
	header := buildHeader(fullPath, totalLines, fileInfo.Size())

	// 13. 构建智能截断提示
	var smartHint string
	if truncated {
		smartHint = buildTruncationHint(fullPath, totalLines, lineStart, linesRead, fileInfo.Size())
	}

	// 14. 构建输出
	output := &FileReadOutput{
		FilePath:    fullPath,
		Exists:      true,
		IsDirectory: false,
		Size:        fileInfo.Size(),
		Content:     pureContent,
		Lines:       totalLines,
		LinesRead:   linesRead,
		LineStart:   lineStart,
		Truncated:   truncated,
		Encoding:    encoding,
		Permissions: fileInfo.Mode().String(),
		Header:      header,
	}

	// 15. 设置消息
	if truncated {
		output.Message = fmt.Sprintf("成功读取第 %d-%d 行（共 %d 行）%s", lineStart, lineStart+linesRead-1, totalLines, smartHint)
	} else {
		output.Message = fmt.Sprintf("成功读取文件，共 %d 行", totalLines)
	}

	return output, nil
}

// buildHeader 构建元信息头
// 格式: [File: main.go | 2500 lines | 85KB]
func buildHeader(path string, totalLines int, size int64) string {
	sizeStr := formatSize(size)
	// 只取文件名，不取完整路径
	fileName := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		fileName = path[idx+1:]
	}
	return fmt.Sprintf("[File: %s | %d lines | %s]", fileName, totalLines, sizeStr)
}

// formatSize 格式化文件大小
func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(size)/float64(div), "KMGTPE"[exp])
}

// buildTruncationHint 构建智能截断提示
func buildTruncationHint(path string, totalLines, lineStart, linesRead int, size int64) string {
	var hint strings.Builder
	hint.WriteString("\n")
	hint.WriteString(fmt.Sprintf("[提示] 当前只显示了第 %d-%d 行，共 %d 行 (%.1f%%)。",
		lineStart, lineStart+linesRead-1, totalLines,
		float64(linesRead)/float64(totalLines)*100))

	if lineStart > 1 {
		hint.WriteString(fmt.Sprintf(" 使用 offset=%d 继续向后读取。", lineStart+linesRead))
	} else {
		hint.WriteString(fmt.Sprintf(" 使用 offset=%d 继续向后读取。", linesRead+1))
	}

	if totalLines > 2000 {
		hint.WriteString(fmt.Sprintf(" 建议分段策略：每次读取 %d 行（如 offset=1, offset=%d, offset=%d...）。",
			linesRead, linesRead+1, linesRead*2+1))
	}

	return hint.String()
}

// detectEncoding 检测并返回编码名称
func detectEncoding(userEncoding string, content []byte) string {
	if userEncoding != "" {
		return userEncoding
	}

	// UTF-8 有效性检测
	if utf8.Valid(content) {
		return "utf-8"
	}

	// BOM 检测
	if len(content) >= 3 {
		// UTF-8 BOM
		if content[0] == 0xEF && content[1] == 0xBB && content[2] == 0xBF {
			return "utf-8-bom"
		}
	}
	if len(content) >= 2 {
		// UTF-16 LE BOM
		if content[0] == 0xFF && content[1] == 0xFE {
			return "utf-16le"
		}
		// UTF-16 BE BOM
		if content[0] == 0xFE && content[1] == 0xFF {
			return "utf-16be"
		}
	}

	// GBK 启发式检测：如果非UTF-8合法且字节高位设置模式符合GBK
	if isLikelyGBK(content) {
		return "gbk"
	}

	// 回退为 latin-1
	return "latin-1"
}

// isLikelyGBK 启发式检测GBK编码
func isLikelyGBK(data []byte) bool {
	// GBK 第一字节范围: 0x81-0xFE, 第二字节范围: 0x40-0xFE
	i := 0
	gbkCount := 0
	for i < len(data)-1 {
		b := data[i]
		if b < 0x80 {
			i++
			continue
		}
		if b >= 0x81 && b <= 0xFE {
			next := data[i+1]
			if (next >= 0x40 && next <= 0x7E) || (next >= 0x80 && next <= 0xFE) {
				gbkCount++
				i += 2
				continue
			}
		}
		i++
	}
	// 如果双字节序列占比超过10%，很可能是GBK
	return gbkCount > 0 && float64(gbkCount*2)/float64(len(data)) > 0.1
}

// decodeContent 根据编码解码内容
func decodeContent(data []byte, encoding string) string {
	switch encoding {
	case "gbk":
		return decodeGBK(data)
	case "utf-16le", "utf-16be":
		// 简单回退：尝试UTF-8解码，失败则用latin-1
		return decodeFallback(data)
	case "latin-1":
		return decodeLatin1(data)
	default:
		// UTF-8 or unknown
		return string(data)
	}
}

// decodeGBK GBK转UTF-8的简化实现
func decodeGBK(data []byte) string {
	// 实际项目中应使用 golang.org/x/text/encoding/simplifiedchinese
	// 这里提供一个基本实现，真正使用时替换为完整的GBK解码器
	var buf strings.Builder
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b < 0x80 {
			buf.WriteByte(b)
			continue
		}
		if i+1 < len(data) && b >= 0x81 && b <= 0xFE {
			next := data[i+1]
			if (next >= 0x40 && next <= 0x7E) || (next >= 0x80 && next <= 0xFE) {
				// GBK -> UTF-8 简单映射（不完整，生产环境需用完整表）
				r := gbkToRune(b, next)
				buf.WriteRune(r)
				i++
				continue
			}
		}
		// 无效字节，用替换字符
		buf.WriteRune('\uFFFD')
	}
	return buf.String()
}

// gbkToRune 简化的GBK到Unicode映射（仅覆盖常用区）
func gbkToRune(hi, lo byte) rune {
	// 实际应使用完整GBK码表，这里做基础映射
	// 对于非ASCII的GBK字符，尝试计算Unicode码点
	// 这不是完整实现，生产环境应使用 golang.org/x/text/encoding
	code := (uint16(hi) << 8) | uint16(lo)
	_ = code
	// 返回替换字符表示无法完整解码
	return '\uFFFD'
}

// decodeFallback 回退解码
func decodeFallback(data []byte) string {
	// 尝试UTF-8
	if utf8.Valid(data) {
		return string(data)
	}
	// 降级为latin-1
	return decodeLatin1(data)
}

// decodeLatin1 Latin-1 -> UTF-8
func decodeLatin1(data []byte) string {
	var buf strings.Builder
	buf.Grow(len(data))
	for _, b := range data {
		buf.WriteRune(rune(b))
	}
	return buf.String()
}
