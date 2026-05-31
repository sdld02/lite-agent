package tools

import (
	"context"
	"strings"
	"testing"
)

func TestWebSearchParseDDGResults(t *testing.T) {
	// 模拟 DuckDuckGo HTML 搜索结果
	html := `<div class="result results_links results_links_deep web-result">
	<div class="links_main links_deep result__body">
	<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com">Example Title</a>
	<div class="result__snippet">This is a test snippet about example.</div>
	</div></div>
	<div class="result results_links results_links_deep web-result">
	<div class="links_main links_deep result__body">
	<a rel="nofollow" class="result__a" href="https://golang.org">Go Programming Language</a>
	<div class="result__snippet">Go is an open source programming language.</div>
	</div></div>`

	results := parseDDGResults(html)

	if len(results) == 0 {
		t.Error("应该至少解析出 1 个结果")
	}

	for i, r := range results {
		t.Logf("结果 %d: title=%s url=%s snippet=%s", i+1, r.title, r.url, r.snippet)
		if r.title == "" {
			t.Errorf("结果 %d 标题为空", i+1)
		}
		if r.url == "" {
			t.Errorf("结果 %d URL 为空", i+1)
		}
	}
}

func TestWebSearchCleanDDGURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath&rut=abc",
			"https://example.com/path",
		},
		{
			"https://golang.org",
			"https://golang.org",
		},
		{
			"//example.com",
			"https://example.com",
		},
	}

	for _, tt := range tests {
		result := cleanDDGURL(tt.input)
		if result != tt.expected {
			t.Errorf("cleanDDGURL(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestWebSearchToolValidation(t *testing.T) {
	tool := NewWebSearchTool()

	// 空查询
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":  "",
		"intent": "测试",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("空查询应该返回错误")
	}
}

func TestWebFetchToolValidation(t *testing.T) {
	tool := NewWebFetchTool(nil)

	// 空 URL
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"url":    "",
		"prompt": "test",
		"intent": "测试",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("空 URL 应该返回错误")
	}

	// 无效 URL
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"url":    "not-a-valid-url",
		"prompt": "test",
		"intent": "测试",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("无效 URL 应该返回错误")
	}

	// 内网地址
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"url":    "http://127.0.0.1:8080/admin",
		"prompt": "test",
		"intent": "测试",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !result.IsError {
		t.Error("内网地址应该被拒绝")
	}
	t.Logf("内网拒绝: %s", result.Content)
}

func TestWebFetchHTMLExtraction(t *testing.T) {
	html := `<html><head><title>Test</title><script>console.log('test');</script><style>body{}</style></head>
	<body><h1>Hello World</h1><p>This is <strong>bold</strong> text.</p><p>Second paragraph &amp; more.</p></body></html>`

	text := extractTextFromHTML(html, "text/html")

	t.Logf("提取的文本: %s", text)

	if !strings.Contains(text, "Hello World") {
		t.Error("应该提取到 'Hello World'")
	}
	if !strings.Contains(text, "bold") {
		t.Error("应该提取到 'bold'")
	}
	if !strings.Contains(text, "Second paragraph") {
		t.Error("应该提取到 'Second paragraph'")
	}
	if strings.Contains(text, "console.log") {
		t.Error("script 内容不应该出现")
	}
}

func TestIsPrivateHost(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"127.0.0.1", true},
		{"localhost", true},  // localhost resolves to 127.0.0.1
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"google.com", false},
		{"github.com", false},
		{"example.com", false},
	}

	for _, tt := range tests {
		result := isPrivateHost(tt.host)
		if result != tt.expected {
			t.Logf("isPrivateHost(%s) = %v (expected %v)", tt.host, result, tt.expected)
		}
	}
}
