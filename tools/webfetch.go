package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"lite-agent/agent"
)

// ============================================================================
// WebFetchTool — 抓取 URL 内容并用 LLM 分析（参考 Claude Code WebFetchTool）
// 纯 Go 实现，支持 HTML→文本转换、15分钟缓存、安全检查
// ============================================================================

const (
	maxFetchContentLength = 10 * 1024 * 1024 // 10MB
	maxFetchOutputChars   = 100_000           // 100K 字符截断
	fetchTimeout          = 60 * time.Second
	maxFetchRedirects     = 10
	fetchCacheTTL         = 15 * time.Minute
)

// WebFetchTool URL 内容抓取与分析工具
type WebFetchTool struct {
	httpClient *http.Client
	cache      map[string]*fetchCacheEntry
	cacheMu    sync.RWMutex
	provider   agent.LLMProvider // 用于分析内容的 LLM
}

// fetchCacheEntry 缓存条目
type fetchCacheEntry struct {
	content string
	expires time.Time
}

// NewWebFetchTool 创建 WebFetchTool 实例
func NewWebFetchTool(provider agent.LLMProvider) *WebFetchTool {
	return &WebFetchTool{
		httpClient: &http.Client{
			Timeout: fetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxFetchRedirects {
					return fmt.Errorf("too many redirects (max %d)", maxFetchRedirects)
				}
				// 只允许同域重定向
				if via[0].URL.Host != req.URL.Host {
					return http.ErrUseLastResponse // 不跟随跨域重定向
				}
				return nil
			},
		},
		cache:    make(map[string]*fetchCacheEntry),
		provider: provider,
	}
}

func (t *WebFetchTool) Name() string {
	return "web_fetch"
}

func (t *WebFetchTool) Description() string {
	return `从指定 URL 抓取内容并使用 AI 进行分析。

用法：
- 抓取网页内容并根据你的问题进行分析和提取
- 支持 HTML 页面（自动转为纯文本）
- HTTP URL 自动升级为 HTTPS
- 结果可能因内容过大而被截断
- 包含 15 分钟缓存，重复访问同一 URL 更快
- 对于 GitHub URL，优先使用 GitHub CLI 工具（gh pr view, gh issue view）

安全限制：
- 禁止访问内网 IP（127.0.0.1、10.x.x.x、192.168.x.x 等）
- 最多跟随 10 次重定向
- 内容超过 10MB 会被截断
- 跨域重定向不会自动跟随，会提示使用重定向后的 URL 重新请求`
}

func (t *WebFetchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "要抓取内容的 URL 地址",
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "你想从页面中提取什么信息？例如：'总结这篇文章的主要内容'、'提取页面中的代码示例'",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如：获取 React 文档中 useEffect 的用法",
			},
		},
		"required": []string{"url", "prompt", "intent"},
	}
}

// 内网 IP 范围（防 SSRF）
var privateCIDRs = []string{
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16", // link-local
	"::1/128",         // IPv6 loopback
	"fc00::/7",        // IPv6 unique local
	"fe80::/10",       // IPv6 link-local
}

func isPrivateHost(host string) bool {
	// 解析主机名到 IP
	ips, err := net.LookupIP(host)
	if err != nil {
		// 无法解析的主机名，保守处理：允许（可能是公网域名）
		return false
	}

	parsedCIDRs := make([]*net.IPNet, 0, len(privateCIDRs))
	for _, cidr := range privateCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			parsedCIDRs = append(parsedCIDRs, ipNet)
		}
	}

	for _, ip := range ips {
		for _, ipNet := range parsedCIDRs {
			if ipNet.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func (t *WebFetchTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 1. 解析参数
	urlStr, ok := args["url"].(string)
	if !ok || urlStr == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("url 参数必须是非空字符串"), IsError: true}, nil
	}
	prompt, ok := args["prompt"].(string)
	if !ok || prompt == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("prompt 参数必须是非空字符串"), IsError: true}, nil
	}

	// 2. 校验 URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("无效的 URL: %s", urlStr)), IsError: true}, nil
	}

	// 防止 SSRF：检查是否为内网地址
	if isPrivateHost(parsedURL.Hostname()) {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("禁止访问内网地址: %s", parsedURL.Hostname())), IsError: true}, nil
	}

	// HTTP → HTTPS 自动升级
	if parsedURL.Scheme == "http" {
		parsedURL.Scheme = "https"
		urlStr = parsedURL.String()
	}

	// 3. 检查缓存
	t.cacheMu.RLock()
	if entry, ok := t.cache[urlStr]; ok && time.Now().Before(entry.expires) {
		result := entry.content
		t.cacheMu.RUnlock()
		return &agent.ToolResult{
			Content:  result,
			RichData: map[string]interface{}{"url": urlStr, "cached": true},
		}, nil
	}
	t.cacheMu.RUnlock()

	// 4. 发起 HTTP 请求
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("创建请求失败: %w", err)), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "lite-agent/1.0")
	req.Header.Set("Accept", "text/html, text/plain, application/json, */*")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("请求失败: %w", err)), IsError: true}, nil
	}
	defer resp.Body.Close()

	// 5. 读取响应（限制大小）
	limitedReader := io.LimitReader(resp.Body, maxFetchContentLength)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("读取响应失败: %w", err)), IsError: true}, nil
	}

	contentType := resp.Header.Get("Content-Type")

	// 6. HTML → 纯文本
	textContent := extractTextFromHTML(string(bodyBytes), contentType)

	// 7. 截断
	if len(textContent) > maxFetchOutputChars {
		textContent = textContent[:maxFetchOutputChars] + "\n\n[内容过长，已截断...]"
	}

	if len(strings.TrimSpace(textContent)) == 0 {
		return &agent.ToolResult{
			Content: fmt.Sprintf("页面内容为空或无法提取文本。HTTP %d %s\nContent-Type: %s\n原始大小: %d bytes",
				resp.StatusCode, resp.Status, contentType, len(bodyBytes)),
		}, nil
	}

	// 8. 用 LLM 分析内容
	analysisPrompt := fmt.Sprintf(`网页内容:
---
%s
---

用户问题: %s

请根据上面的网页内容回答用户的问题。要求:
- 严格基于网页内容回答，不要编造信息
- 回答应简洁、准确
- 如果网页内容不足以回答问题，请明确指出`, textContent, prompt)

	analysisResult := "未能获取分析结果"
	if t.provider != nil {
		messages := []agent.Message{
			{Role: "user", Content: analysisPrompt},
		}
		respMsg, err := t.provider.Chat(ctx, messages, nil)
		if err == nil && respMsg != nil {
			analysisResult = respMsg.Content
		}
	} else {
		// 无 LLM 时直接返回原始文本片段
		analysisResult = fmt.Sprintf("（无 LLM 分析）原始内容摘要:\n\n%s", textContent[:min(len(textContent), 2000)])
	}

	// 9. 构建结果
	durationMs := time.Since(start).Milliseconds()
	finalResult := fmt.Sprintf("%s\n\n---\nURL: %s | HTTP %d | 大小: %d bytes | 耗时: %dms",
		analysisResult, urlStr, resp.StatusCode, len(bodyBytes), durationMs)

	// 10. 缓存结果
	t.cacheMu.Lock()
	t.cache[urlStr] = &fetchCacheEntry{
		content: finalResult,
		expires: time.Now().Add(fetchCacheTTL),
	}
	// 清理过期条目
	for k, v := range t.cache {
		if time.Now().After(v.expires) {
			delete(t.cache, k)
		}
	}
	t.cacheMu.Unlock()

	return &agent.ToolResult{
		Content: finalResult,
		RichData: map[string]interface{}{
			"url":        urlStr,
			"statusCode": resp.StatusCode,
			"bytes":      len(bodyBytes),
			"durationMs": durationMs,
		},
	}, nil
}

// ============================================================================
// HTML 文本提取
// ============================================================================

var (
	htmlTagRe   = regexp.MustCompile(`<[^>]*>`)
	htmlSpaceRe = regexp.MustCompile(`\s+`)
	scriptRe    = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRe     = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	commentRe   = regexp.MustCompile(`<!--.*?-->`)
	entityRe    = regexp.MustCompile(`&[a-zA-Z]+;`)
)

func extractTextFromHTML(rawHTML, contentType string) string {
	if rawHTML == "" {
		return ""
	}

	// 非 HTML 内容直接返回（但截断）
	if contentType != "" && !strings.Contains(contentType, "text/html") {
		if len(rawHTML) > maxFetchOutputChars {
			rawHTML = rawHTML[:maxFetchOutputChars]
		}
		return rawHTML
	}

	// 去掉 script、style、注释
	text := scriptRe.ReplaceAllString(rawHTML, "")
	text = styleRe.ReplaceAllString(text, "")
	text = commentRe.ReplaceAllString(text, "")

	// 去掉 HTML 标签
	text = htmlTagRe.ReplaceAllString(text, " ")

	// 解码 HTML 实体
	text = html.UnescapeString(text)

	// 去掉实体引用残留
	text = entityRe.ReplaceAllString(text, " ")

	// 压缩空白
	text = htmlSpaceRe.ReplaceAllString(text, " ")

	// 去除首尾空白
	text = strings.TrimSpace(text)

	return text
}

// ============================================================================
// 辅助
// ============================================================================

// SetProvider 设置 LLM 提供者（用于内容分析）
func (t *WebFetchTool) SetProvider(provider agent.LLMProvider) {
	t.provider = provider
}
