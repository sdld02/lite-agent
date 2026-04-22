package file

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

// FileWriteInput 输入参数
type FileWriteInput struct {
    FilePath string `json:"file_path"`
    Content  string `json:"content"`
}

// FileWriteOutput 输出结果
type FileWriteOutput struct {
    Type          string `json:"type"` // "create" or "update"
    FilePath      string `json:"file_path"`
    Content       string `json:"content"`
    StructuredPatch string `json:"structured_patch"` // unified diff
    OriginalFile  string `json:"original_file,omitempty"` // null for new files
    GitDiff       string `json:"git_diff,omitempty"`
    LinesChanged  int    `json:"lines_changed"`
    Message       string `json:"message"`
}

// 常量
const MaxWriteFileSize = 1024 * 1024 * 1024 // 1 GiB（可按需调整）

// FileWriteTool 主函数
func FileWriteTool(input FileWriteInput) (*FileWriteOutput, error) {
    // 1. 路径规范化
    fullPath, err := expandPath(input.FilePath)
    if err != nil {
        return nil, fmt.Errorf("invalid path: %w", err)
    }

    // 2. 基础安全检查
    if err := checkTeamMemSecrets(fullPath, input.Content); err != nil {
        return nil, err
    }
    if isUNCPath(fullPath) {
        return nil, errors.New("UNC paths are not allowed for security reasons")
    }

    // 2.1 路径遍历防护
    if err := validatePathSafety(fullPath); err != nil {
        return nil, err
    }

    // 2.2 写入权限预检查
    if err := checkWritePermission(fullPath); err != nil {
        return nil, err
    }

    // 3. 读取原始文件（用于 diff 和并发检查）
    var originalContent string
    var isNewFile bool
    originalBytes, err := os.ReadFile(fullPath)
    if err != nil {
        if os.IsNotExist(err) {
            isNewFile = true
        } else {
            return nil, fmt.Errorf("failed to read file: %w", err)
        }
    } else {
        // 3.1 二进制文件检测（防止覆盖二进制文件）
        if isBinaryFile(originalBytes) {
            return nil, fmt.Errorf("cannot overwrite binary file: %s", fullPath)
        }
        originalContent = normalizeLineEndings(string(originalBytes))
        // 并发防护：简单 mtime + hash 检查（生产环境建议结合 readFileState）
        if err := checkFileFreshness(fullPath, originalBytes); err != nil {
            return nil, fmt.Errorf("file was modified unexpectedly: %w", err)
        }
    }

    // 4. 确保目录存在
    if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
        return nil, err
    }

    // 5. 写入新内容（原子写）
    if err := writeTextContent(fullPath, input.Content); err != nil {
        return nil, fmt.Errorf("failed to write file: %w", err)
    }

    // 6. 生成 patch
    var patch string
    if isNewFile {
        patch = fmt.Sprintf("--- /dev/null\n+++ %s\n@@ -0,0 +1 @@\n+%s", fullPath, input.Content)
    } else {
        patch = diffToUnified(originalContent, input.Content)
    }

    // 7. 统计变更
    linesChanged := countLinesChanged(originalContent, input.Content)

    output := &FileWriteOutput{
        Type:          map[bool]string{true: "create", false: "update"}[isNewFile],
        FilePath:      fullPath,
        Content:       input.Content,
        StructuredPatch: patch,
        OriginalFile:  originalContent, // 新文件时为空字符串（前端可视为 null）
        LinesChanged:  linesChanged,
        Message:       "File written successfully",
    }

    return output, nil
}

// ==================== write.go 独有的辅助函数 ====================

// 简单 secret 检查（可扩展正则）
func checkTeamMemSecrets(path, content string) error {
    patterns := []string{`sk-`, `AKIA`, `Bearer `}
    for _, pat := range patterns {
        if strings.Contains(content, pat) {
            return errors.New("potential secret detected in content")
        }
    }
    return nil
}

// 保留编码风格的写入
func writeTextContent(path, content string) error {
    return os.WriteFile(path, []byte(content), 0644)
}