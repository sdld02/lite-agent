package tools

import (
	"context"
	"os"
	"testing"
)

func TestGrepToolFilesWithMatches(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func.*GrepTool",
		"path":    ".",
		"glob":    "*.go",
		"intent":  "测试：搜索包含 GrepTool 的 Go 文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}
	t.Logf("files_with_matches 结果:\n%s", result.Content)

	if r, ok := result.RichData.(map[string]interface{}); ok {
		t.Logf("numFiles: %v", r["numFiles"])
	}
}

func TestGrepToolContentMode(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "GrepTool",
		"path":        ".",
		"glob":        "grep.go",
		"output_mode": "content",
		"-n":          true,
		"intent":      "测试：显示 grep.go 中包含 GrepTool 的行",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}
	t.Logf("content 模式结果:\n%s", result.Content)
}

func TestGrepToolCountMode(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "func",
		"path":        ".",
		"glob":        "grep.go",
		"output_mode": "count",
		"intent":      "测试：统计 grep.go 中 func 出现次数",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}
	t.Logf("count 模式结果:\n%s", result.Content)
}

func TestGrepToolCaseInsensitive(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "grepTool",
		"path":        ".",
		"glob":        "grep.go",
		"output_mode": "content",
		"-i":          true,
		"intent":      "测试：大小写不敏感搜索",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}
	if result.Content == "没有找到匹配结果" {
		t.Error("大小写不敏感搜索应该能匹配到 GrepTool")
	}
	t.Logf("大小写不敏感结果:\n%s", result.Content)
}

func TestGrepToolHeadLimit(t *testing.T) {
	tool := NewGrepTool()

	// 不限路径下的所有 .go 文件
	cwd, _ := os.Getwd()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "import",
		"path":        cwd,
		"glob":        "*.go",
		"output_mode": "content",
		"head_limit":  float64(5),
		"intent":      "测试：head_limit 限制输出",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	// 计数输出行，不应超过 5 + pagination 行
	lines := 0
	for _, c := range result.Content {
		if c == '\n' {
			lines++
		}
	}
	if lines > 8 { // 5 个结果行 + pagination info
		t.Errorf("head_limit=5 但输出了 %d 行", lines)
	}
	t.Logf("head_limit 结果 (%d 行):\n%s", lines, result.Content)
}

func TestGrepToolNonexistentPath(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "test",
		"path":    "/nonexistent/path/xyz",
		"intent":  "测试：不存在的路径",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("不存在的路径应返回错误")
	}
	t.Logf("错误结果: %s", result.Content)
}

func TestGrepToolInvalidRegex(t *testing.T) {
	tool := NewGrepTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "[invalid",
		"path":    ".",
		"intent":  "测试：无效正则表达式",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("无效正则表达式应返回错误")
	}
	t.Logf("错误结果: %s", result.Content)
}
