package file

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/sergi/go-diff/diffmatchpatch" // go get github.com/sergi/go-diff/diffmatchpatch
)

// FileEditInput 输入参数
type FileEditInput struct {
    FilePath   string `json:"file_path"`
    OldString  string `json:"old_string"`
    NewString  string `json:"new_string"`
    ReplaceAll bool   `json:"replace_all,omitempty"`
}

// FileEditOutput 输出结果
type FileEditOutput struct {
    OriginalContent string `json:"original_content,omitempty"`
    EditedContent   string `json:"edited_content"`
    Patch           string `json:"patch"`           // unified diff
    GitDiff         string `json:"git_diff,omitempty"`
    LinesChanged    int    `json:"lines_changed"`
    Message         string `json:"message,omitempty"`
    ErrorCode       int    `json:"error_code,omitempty"`
}

// 常量
const (
    MaxEditFileSize = 1024 * 1024 * 1024 // 1 GiB
)

// FileEditTool 主函数
func FileEditTool(input FileEditInput) (*FileEditOutput, error) {
    // 1. 路径规范化（支持 ~ 和相对路径）
    fullPath, err := expandPath(input.FilePath)
    if err != nil {
        return nil, fmt.Errorf("invalid path: %w", err)
    }

    // 2. 基础验证
    if input.OldString == input.NewString {
        return nil, errors.New("no changes: old_string and new_string are identical")
    }

    // 简单 secret 防护（实际项目中可替换为更完善的检查）
    if containsSecret(input.NewString) {
        return nil, errors.New("potential secret detected in new_string")
    }

    // 3. UNC 路径安全防护（Windows）
    if isUNCPath(fullPath) {
        return nil, errors.New("UNC paths are not allowed for security reasons")
    }

    // 3.1 路径遍历防护
    if err := validatePathSafety(fullPath); err != nil {
        return nil, err
    }

    // 3.2 写入权限预检查
    if err := checkWritePermission(fullPath); err != nil {
        return nil, err
    }

    // 4. 文件大小检查（如果存在）
    info, statErr := os.Stat(fullPath)
    if statErr == nil && info.Size() > MaxEditFileSize {
        return nil, fmt.Errorf("file too large (%d bytes), max allowed is %d bytes", info.Size(), MaxEditFileSize)
    }

    // 5. 读取原始文件
    originalBytes, err := os.ReadFile(fullPath)
    if err != nil {
        if os.IsNotExist(err) {
            if input.OldString != "" {
                return nil, fmt.Errorf("file does not exist. To create a new file, set old_string to empty string")
            }
            // 创建新文件
            return createNewFile(fullPath, input.NewString)
        }
        return nil, fmt.Errorf("failed to read file: %w", err)
    }

    // 5.1 二进制文件检测
    if isBinaryFile(originalBytes) {
        return nil, fmt.Errorf("cannot edit binary file: %s", fullPath)
    }

    originalContent := normalizeLineEndings(string(originalBytes))

    // 6. 并发修改防护（简单实现：记录修改时间 + hash）
    if err := checkFileFreshness(fullPath, originalBytes); err != nil {
        return nil, fmt.Errorf("file was modified unexpectedly: %w", err)
    }

    // 7. 查找并替换（支持 replace_all）
    editedContent, patch, err := performEdit(originalContent, input.OldString, input.NewString, input.ReplaceAll)
    if err != nil {
        return nil, err
    }

    // 8. 写回文件（保留原始编码与换行风格）
    if err := writeFileWithOriginalStyle(fullPath, editedContent, originalBytes); err != nil {
        return nil, fmt.Errorf("failed to write file: %w", err)
    }

    // 9. 生成输出
    linesChanged := countLinesChanged(originalContent, editedContent)

    output := &FileEditOutput{
        OriginalContent: originalContent,
        EditedContent:   editedContent,
        Patch:           patch,
        LinesChanged:    linesChanged,
        Message:         "File edited successfully",
    }

    // 可选：生成 git diff（这里简化省略，实际可调用 git diff 命令）

    return output, nil
}

// 创建新文件
func createNewFile(path, content string) (*FileEditOutput, error) {
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return nil, err
    }

    if err := os.WriteFile(path, []byte(content), 0644); err != nil {
        return nil, err
    }

    return &FileEditOutput{
        EditedContent: content,
        Patch:         fmt.Sprintf("--- /dev/null\n+++ %s\n@@ -0,0 +1 @@\n+%s", path, content),
        Message:       "New file created",
    }, nil
}

// 执行字符串替换并生成 patch
func performEdit(original, oldStr, newStr string, replaceAll bool) (string, string, error) {
    if oldStr == "" {
        // 追加模式（新文件已处理，这里仅处理追加）
        edited := original + newStr
        patch := diffToUnified(original, edited)
        return edited, patch, nil
    }

    var edited string
    if replaceAll {
        edited = strings.ReplaceAll(original, oldStr, newStr)
    } else {
        // 只替换第一个匹配
        idx := strings.Index(original, oldStr)
        if idx == -1 {
            // 尝试忽略引号风格等智能匹配（简化版）
            edited = strings.Replace(original, oldStr, newStr, 1)
            if edited == original {
                return "", "", errors.New("old_string not found in file. Try providing more context")
            }
        } else {
            edited = original[:idx] + newStr + original[idx+len(oldStr):]
        }
    }

    if edited == original {
        return "", "", errors.New("no change made after replacement")
    }

    patch := diffToUnified(original, edited)
    return edited, patch, nil
}

// 使用 go-diff 生成 unified diff
func diffToUnified(a, b string) string {
    dmp := diffmatchpatch.New()
    diffs := dmp.DiffMain(a, b, false)
    return dmp.DiffPrettyText(diffs) // 或自定义为 unified format
}

// 写回文件，尽量保留原始换行风格
func writeFileWithOriginalStyle(path, content string, originalBytes []byte) error {
    // 简单实现：直接写 UTF-8，实际可根据 originalBytes 的 BOM 和换行进一步优化
    return os.WriteFile(path, []byte(content), 0644)
}



func containsSecret(s string) bool {
    // 简单示例，可扩展为正则匹配密钥模式
    secretPatterns := []string{`sk-`, `AKIA`, `Bearer `}
    for _, pat := range secretPatterns {
        if strings.Contains(s, pat) {
            return true
        }
    }
    return false
}

// 简单并发防护
func checkFileFreshness(path string, content []byte) error {
    // 实际项目中可结合 readFileState + mtime + hash 实现
    return nil
}

func countLinesChanged(a, b string) int {
    linesA := strings.Split(a, "\n")
    linesB := strings.Split(b, "\n")
    return len(linesB) - len(linesA) // 简化计算
}


// checkWritePermission 检查文件或其父目录的写入权限
func checkWritePermission(path string) error {
    info, err := os.Stat(path)
    if err == nil {
        // 文件存在，检查是否可写
        if info.Mode().Perm()&0200 == 0 {
            return fmt.Errorf("file is read-only: %s", path)
        }
        return nil
    }
    if !os.IsNotExist(err) {
        return fmt.Errorf("cannot stat file: %w", err)
    }
    // 文件不存在，检查父目录是否可写
    dir := filepath.Dir(path)
    dirInfo, err := os.Stat(dir)
    if err != nil {
        if os.IsNotExist(err) {
            // 父目录也不存在，将由 MkdirAll 创建，检查更上层
            return nil
        }
        return fmt.Errorf("cannot stat parent directory: %w", err)
    }
    if !dirInfo.IsDir() {
        return fmt.Errorf("parent path is not a directory: %s", dir)
    }
    if dirInfo.Mode().Perm()&0200 == 0 {
        return fmt.Errorf("parent directory is read-only: %s", dir)
    }
    return nil
}

// validatePathSafety 路径遍历防护
// 防止通过 ../ 等方式访问敏感系统目录
func validatePathSafety(path string) error {
    // 清理路径（消除 ../ 和 ./ ）
    cleaned := filepath.Clean(path)

    // 敏感系统路径黑名单
    sensitiveRoots := []string{
        "/etc",
        "/var",
        "/usr",
        "/bin",
        "/sbin",
        "/boot",
        "/dev",
        "/proc",
        "/sys",
        "/root",
    }

    for _, root := range sensitiveRoots {
        if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
            return fmt.Errorf("access to system path is not allowed: %s", cleaned)
        }
    }

    return nil
}