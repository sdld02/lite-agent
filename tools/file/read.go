package file

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// FileReadInput 输入参数
type FileReadInput struct {
	FilePath string `json:"file_path"`
	MaxLines int    `json:"max_lines,omitempty"` // 最大读取行数，0表示无限制
	Encoding string `json:"encoding,omitempty"`  // 文件编码，默认utf-8
}

// FileReadOutput 输出结果
type FileReadOutput struct {
	FilePath     string `json:"file_path"`
	Exists       bool   `json:"exists"`
	IsDirectory  bool   `json:"is_directory,omitempty"`
	Size         int64  `json:"size,omitempty"`          // 文件大小（字节）
	Content      string `json:"content,omitempty"`       // 文件内容
	Lines        int    `json:"lines,omitempty"`         // 总行数
	LinesRead    int    `json:"lines_read,omitempty"`    // 实际读取的行数
	Truncated    bool   `json:"truncated,omitempty"`     // 是否被截断
	Encoding     string `json:"encoding,omitempty"`      // 实际使用的编码
	ErrorMessage string `json:"error_message,omitempty"` // 错误信息（如果读取失败）
	Permissions  string `json:"permissions,omitempty"`   // 文件权限
	Message      string `json:"message"`                 // 状态消息
}

// 常量
const (
	MaxReadFileSize = 1024 * 1024 * 10 // 10 MB（可按需调整）
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
			Message:      "文件过大，无法读取",
		}, nil
	}

	// 6. 设置最大行数
	maxLines := input.MaxLines
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}

	// 7. 读取文件内容
	content, err := os.ReadFile(fullPath)
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

	// 8. 处理编码（简化版本，假设是UTF-8）
	encoding := input.Encoding
	if encoding == "" {
		encoding = "utf-8"
	}

	// 9. 转换为字符串并处理行数
	contentStr := string(content)
	lines := strings.Split(contentStr, "\n")
	totalLines := len(lines)

	// 10. 如果行数超过限制，进行截断
	truncated := false
	linesRead := totalLines
	if totalLines > maxLines {
		lines = lines[:maxLines]
		linesRead = maxLines
		truncated = true
		contentStr = strings.Join(lines, "\n")
	}

	// 11. 构建输出
	output := &FileReadOutput{
		FilePath:    fullPath,
		Exists:      true,
		IsDirectory: false,
		Size:        fileInfo.Size(),
		Content:     contentStr,
		Lines:       totalLines,
		LinesRead:   linesRead,
		Truncated:   truncated,
		Encoding:    encoding,
		Permissions: fileInfo.Mode().String(),
	}

	// 12. 设置消息
	if truncated {
		output.Message = fmt.Sprintf("成功读取文件，显示前 %d 行（共 %d 行）", linesRead, totalLines)
	} else {
		output.Message = fmt.Sprintf("成功读取文件，共 %d 行", totalLines)
	}

	return output, nil
}

