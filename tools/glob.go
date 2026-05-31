package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lite-agent/agent"
)

// ============================================================================
// GlobTool — 纯 Go 实现的文件名 glob 匹配工具（参考 Claude Code GlobTool 设计）
// 零外部依赖，跨平台可用，支持 ** 递归匹配
// ============================================================================

// 默认最大返回文件数
const defaultGlobLimit = 100

// GlobTool 文件名模式匹配工具
type GlobTool struct{}

// NewGlobTool 创建 GlobTool 实例
func NewGlobTool() *GlobTool {
	return &GlobTool{}
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return `快速文件名模式匹配工具，适用于任何规模的代码库。

用法：
- 支持 glob 模式匹配文件名，如 "**/*.go" 或 "src/**/*_test.go"
- 返回匹配的文件路径列表，按修改时间排序（最近修改的在前）
- 默认最多返回 100 个文件，超出会标记为截断
- 自动排除 .git/node_modules 等常见非代码目录
- 当需要按文件名模式查找文件时使用此工具
- 当需要搜索文件内容时，请使用 grep 工具
- 当需要进行多轮 glob+grep 的开放式搜索时，请使用 Agent 工具`
}

func (t *GlobTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob 模式，如 '**/*.go', 'src/**/*_test.go', '*.md'。支持 ** 递归匹配、* 通配符、? 单字符匹配。",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索的目录路径。如果不指定，默认使用当前工作目录。必须是一个有效的目录路径。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "最大返回文件数。默认 100。",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如：查找所有 Go 源文件",
			},
		},
		"required": []string{"pattern", "intent"},
	}
}

// globResult 单个匹配的文件信息
type globResult struct {
	path    string
	mtime   time.Time
}

func (t *GlobTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 1. 解析参数
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("pattern 参数必须是非空字符串"), IsError: true}, nil
	}

	searchPath, _ := args["path"].(string)
	if searchPath == "" {
		var err error
		searchPath, err = os.Getwd()
		if err != nil {
			return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("无法获取当前工作目录: %w", err)), IsError: true}, nil
		}
	}

	// 验证搜索路径
	absPath, err := filepath.Abs(searchPath)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("路径无效: %s", searchPath)), IsError: true}, nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("路径不存在: %s", searchPath)), IsError: true}, nil
	}
	if !info.IsDir() {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("路径不是目录: %s", searchPath)), IsError: true}, nil
	}

	limit := defaultGlobLimit
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	// 2. 执行 glob 搜索
	start := time.Now()
	var results []globResult

	filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			return nil
		}

		// 跳过目录
		if info.IsDir() {
			base := info.Name()
			// 跳过 VCS 和常见忽略目录
			if grepExcludeDirs[base] || (len(base) > 0 && base[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}

		// 跳过二进制文件
		ext := strings.ToLower(filepath.Ext(path))
		if binaryExtensions[ext] {
			return nil
		}

		// 获取相对路径进行匹配
		relPath, _ := filepath.Rel(absPath, path)

		// glob 匹配
		if matchGlob(pattern, relPath) {
			results = append(results, globResult{
				path:  relPath,
				mtime: info.ModTime(),
			})
		}

		return nil
	})

	// 3. 按修改时间降序排列
	sort.Slice(results, func(i, j int) bool {
		return results[i].mtime.After(results[j].mtime)
	})

	// 4. 截断
	truncated := len(results) > limit
	if truncated {
		results = results[:limit]
	}

	// 5. 格式化输出
	durationMs := time.Since(start).Milliseconds()
	filenames := make([]string, len(results))
	for i, r := range results {
		filenames[i] = r.path
	}

	if len(filenames) == 0 {
		return &agent.ToolResult{
			Content: "没有找到匹配的文件",
			RichData: map[string]interface{}{
				"durationMs": durationMs,
				"numFiles":   0,
				"filenames":  []string{},
				"truncated":  false,
			},
		}, nil
	}

	result := strings.Join(filenames, "\n")
	if truncated {
		result += "\n(结果已截断，请使用更具体的路径或模式)"
	}

	return &agent.ToolResult{
		Content: result,
		RichData: map[string]interface{}{
			"durationMs": durationMs,
			"numFiles":   len(filenames),
			"filenames":  filenames,
			"truncated":  truncated,
		},
	}, nil
}

// ============================================================================
// Glob 匹配器（支持 ** 递归匹配）
// ============================================================================

// matchGlob 测试路径是否匹配 glob 模式
// 支持:
//   - ** 匹配零个或多个目录层
//   - * 匹配文件名中的任意字符（不含路径分隔符）
//   - ? 匹配单个字符
//   - 复用 filepath.Match 处理单层匹配
func matchGlob(pattern, path string) bool {
	// 将 \ 统一为 /（Windows 兼容）
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// 如果没有 **，直接用 filepath.Match（它不跨目录层）
	if !strings.Contains(pattern, "**") {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
		// 也尝试只匹配文件名（兼容 "*.go" 这种简写）
		matched, err = filepath.Match(pattern, filepath.Base(path))
		return err == nil && matched
	}

	// 包含 **，需要特殊处理
	return matchDoubleStar(pattern, path)
}

// matchDoubleStar 支持 ** 的递归匹配
// 算法：将模式按 ** 分割，用前缀+后缀+剩余部分进行匹配
func matchDoubleStar(pattern, path string) bool {
	// 分割 pattern 和 path
	patParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	// 使用动态规划：dp[i][j] = pattern[:i] 是否匹配 path[:j]
	// 但这里用递归+缓存的简化版本更直观
	return matchSegments(patParts, pathParts)
}

// matchSegments 递归匹配模式段和路径段
// pat[i:] 匹配 path[j:]
func matchSegments(pat, path []string) bool {
	// 缓存键
	return matchSegmentsDP(pat, path)
}

// matchSegmentsDP 使用 DP 进行分段匹配
func matchSegmentsDP(pat, path []string) bool {
	pLen, ppLen := len(pat), len(path)

	// dp[i][j] = pat[0:i] matches path[0:j]
	dp := make([][]bool, pLen+1)
	for i := range dp {
		dp[i] = make([]bool, ppLen+1)
	}

	dp[0][0] = true

	// 处理 pattern 开头就是 ** 的情况
	for i := 0; i < pLen && pat[i] == "**"; i++ {
		dp[i+1][0] = true
	}

	for i := 0; i < pLen; i++ {
		for j := 0; j <= ppLen; j++ {
			if !dp[i][j] {
				continue
			}

			if pat[i] == "**" {
				// ** 匹配零个路径段
				dp[i+1][j] = true
				// ** 匹配一个或多个路径段
				for k := j; k <= ppLen; k++ {
					dp[i+1][k] = true
				}
			} else if j < ppLen {
				// 普通匹配：单层 glob
				matched, err := filepath.Match(pat[i], path[j])
				if err == nil && matched {
					dp[i+1][j+1] = true
				}
			}
		}
	}

	return dp[pLen][ppLen]
}
