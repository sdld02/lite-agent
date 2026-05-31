package tools

import (
	"context"
	"testing"
)

func TestGlobToolBasic(t *testing.T) {
	tool := NewGlobTool()

	// 搜索当前目录下所有 .go 文件
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    ".",
		"intent":  "测试：查找当前目录的 Go 文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	if result.Content == "没有找到匹配的文件" {
		t.Error("应该找到至少一个 .go 文件")
	}

	if r, ok := result.RichData.(map[string]interface{}); ok {
		t.Logf("找到 %v 个文件, 耗时 %vms, 截断=%v",
			r["numFiles"], r["durationMs"], r["truncated"])
		filenames := r["filenames"].([]string)
		for _, f := range filenames {
			t.Logf("  %s", f)
		}
	}
}

func TestGlobToolRecursive(t *testing.T) {
	tool := NewGlobTool()

	// 递归搜索所有 .go 文件
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
		"path":    ".",
		"intent":  "测试：递归查找所有 Go 文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	if result.Content == "没有找到匹配的文件" {
		t.Error("应该找到 Go 文件")
	}

	if r, ok := result.RichData.(map[string]interface{}); ok {
		numFiles := r["numFiles"].(int)
		t.Logf("递归找到 %d 个 .go 文件", numFiles)
		if numFiles < 2 {
			t.Error("递归搜索应该找到多个文件")
		}
	}
}

func TestGlobToolSubdirectory(t *testing.T) {
	tool := NewGlobTool()

	// 只查找 tools/ 子目录下
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "grep*.go",
		"path":    ".",
		"intent":  "测试：查找 grep 相关文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	t.Logf("grep*.go 匹配结果:\n%s", result.Content)

	// 应该至少包含 grep.go
	if result.Content != "没有找到匹配的文件" {
		found := false
		for _, c := range result.Content {
			if c == 'g' {
				found = true
				break
			}
		}
		if !found {
			// 检查 RichData
			if r, ok := result.RichData.(map[string]interface{}); ok {
				for _, f := range r["filenames"].([]string) {
					t.Logf("  %s", f)
				}
			}
		}
	}
}

func TestGlobToolTestPattern(t *testing.T) {
	tool := NewGlobTool()

	// 查找所有测试文件
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*_test.go",
		"path":    ".",
		"intent":  "测试：查找所有测试文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	t.Logf("测试文件匹配结果:\n%s", result.Content)
}

func TestGlobToolLimit(t *testing.T) {
	tool := NewGlobTool()

	// 低限制
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
		"path":    ".",
		"limit":   float64(3),
		"intent":  "测试：限制只返回 3 个文件",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	if r, ok := result.RichData.(map[string]interface{}); ok {
		count := r["numFiles"].(int)
		t.Logf("limit=3, 实际返回 %d 个", count)
		if count > 3 {
			t.Errorf("limit=3 但返回了 %d 个文件", count)
		}
	}
}

func TestGlobToolNoMatch(t *testing.T) {
	tool := NewGlobTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.xyznonexistent",
		"path":    ".",
		"intent":  "测试：不可能匹配的模式",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if result.IsError {
		t.Fatalf("工具返回错误: %s", result.Content)
	}

	if result.Content != "没有找到匹配的文件" {
		t.Errorf("不应该找到文件，但返回了: %s", result.Content)
	}
	t.Logf("正确返回: %s", result.Content)
}

func TestGlobToolInvalidPath(t *testing.T) {
	tool := NewGlobTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    "/nonexistent/path/xyz",
		"intent":  "测试：不存在的路径",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("不存在的路径应返回错误")
	}
	t.Logf("错误: %s", result.Content)
}

func TestGlobToolNotDirectory(t *testing.T) {
	tool := NewGlobTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    "glob.go", // 这是一个文件，不是目录
		"intent":  "测试：路径是文件而非目录",
	})

	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("文件路径应返回错误（需要目录）")
	}
	t.Logf("错误: %s", result.Content)
}
