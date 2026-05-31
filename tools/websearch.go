package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"lite-agent/agent"
)

// ============================================================================
// WebSearchTool — DuckDuckGo 搜索引擎（参考 Claude Code WebSearchTool）
// 纯 Go 实现，零外部依赖，抓取 DuckDuckGo HTML 搜索结果
// ============================================================================

const (
	searchTimeout       = 15 * time.Second
	ddgSearchURL        = "https://html.duckduckgo.com/html/"
	maxSearchResults    = 10
)

// WebSearchTool 网页搜索工具
type WebSearchTool struct {
	httpClient *http.Client
}

// NewWebSearchTool 创建 WebSearchTool 实例
func NewWebSearchTool() *WebSearchTool {
	return &WebSearchTool{
		httpClient: &http.Client{
			Timeout: searchTimeout,
		},
	}
}

func (t *WebSearchTool) Name() string {
	return "web_search"
}

func (t *WebSearchTool) Description() string {
	return `在互联网上搜索最新信息。

用法：
- 使用 DuckDuckGo 搜索互联网获取最新信息
- 返回搜索结果的标题、URL 和摘要
- 适用于获取 Claude 知识截止日期之后的信息
- 当前日期和时间会被自动添加到搜索查询中

重要：在回答用户问题时，必须引用搜索结果的来源链接。`
}

func (t *WebSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "搜索查询词",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如：搜索 React 19 新特性",
			},
		},
		"required": []string{"query", "intent"},
	}
}

// searchResult 单条搜索结果
type searchResult struct {
	title   string
	url     string
	snippet string
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 1. 解析参数
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return &agent.ToolResult{Content: agent.FormatValidationError("query 参数必须是非空字符串"), IsError: true}, nil
	}

	// 2. 发起 DuckDuckGo 搜索
	start := time.Now()

	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "us-en") // 英文区域

	req, err := http.NewRequestWithContext(ctx, "POST", ddgSearchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("创建搜索请求失败: %w", err)), IsError: true}, nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "lite-agent/1.0 (web-search)")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("搜索请求失败: %w", err)), IsError: true}, nil
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatToolError(fmt.Errorf("读取搜索响应失败: %w", err)), IsError: true}, nil
	}

	// 3. 解析搜索结果
	results := parseDDGResults(string(bodyBytes))
	durationMs := time.Since(start).Milliseconds()

	if len(results) == 0 {
		return &agent.ToolResult{
			Content: fmt.Sprintf("未找到与 \"%s\" 相关的结果。（耗时 %dms）", query, durationMs),
			RichData: map[string]interface{}{
				"query":     query,
				"results":   []interface{}{},
				"durationMs": durationMs,
			},
		}, nil
	}

	// 4. 格式化输出
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("搜索 \"%s\" 的结果（耗时 %dms）：\n\n", query, durationMs))

	for i, r := range results {
		if i >= maxSearchResults {
			break
		}
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, r.title))
		sb.WriteString(fmt.Sprintf("   URL: %s\n", r.url))
		if r.snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.snippet))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("提醒：请在你的回答中引用上述搜索结果中的来源链接。")

	// 构建 RichData
	richResults := make([]map[string]string, 0, len(results))
	for i, r := range results {
		if i >= maxSearchResults {
			break
		}
		richResults = append(richResults, map[string]string{
			"title":   r.title,
			"url":     r.url,
			"snippet": r.snippet,
		})
	}

	return &agent.ToolResult{
		Content: sb.String(),
		RichData: map[string]interface{}{
			"query":      query,
			"results":    richResults,
			"durationMs": durationMs,
		},
	}, nil
}

// ============================================================================
// DuckDuckGo HTML 结果解析
// ============================================================================

// DDG 搜索结果页面的正则模式
var (
	ddgResultRe = regexp.MustCompile(`<div class="result[^"]*"[^>]*>[\s\S]*?</div>\s*</div>\s*</div>`)
	ddgTitleRe  = regexp.MustCompile(`<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>([^<]+)</a>`)
	ddgSnippetRe = regexp.MustCompile(`<a[^>]*class="result__snippet"[^>]*>([\s\S]*?)</a>`)
	ddgLinkRe   = regexp.MustCompile(`class="result__url"[^>]*>\s*([^<]+)\s*<`)
)

func parseDDGResults(htmlContent string) []searchResult {
	var results []searchResult

	// 找到所有搜索结果块
	matches := ddgResultRe.FindAllString(htmlContent, -1)
	if len(matches) == 0 {
		// 尝试更宽松的匹配
		matches = findResultBlocks(htmlContent)
	}

	for _, block := range matches {
		var r searchResult

		// 提取标题和 URL
		titleMatch := ddgTitleRe.FindStringSubmatch(block)
		if titleMatch != nil && len(titleMatch) >= 3 {
			// URL 可能需要解码
			r.url = cleanDDGURL(titleMatch[1])
			r.title = cleanHTMLText(titleMatch[2])
		} else {
			// 尝试备用提取方式
			r.title, r.url = extractTitleAndURL(block)
		}

		if r.title == "" {
			continue
		}

		// 提取摘要
		snippetMatch := ddgSnippetRe.FindStringSubmatch(block)
		if snippetMatch != nil && len(snippetMatch) >= 2 {
			r.snippet = cleanHTMLText(snippetMatch[1])
		}

		// 清理并去重
		if !isDuplicate(results, r) {
			results = append(results, r)
		}
	}

	return results
}

// findResultBlocks 更宽松的结果块查找（DDG 可能改变 HTML 结构）
func findResultBlocks(html string) []string {
	var blocks []string
	// 查找包含 result__a 链接的 div 块
	parts := strings.Split(html, `<a rel="nofollow" class="result__a"`)
	for i, part := range parts {
		if i == 0 {
			continue
		}
		// 找到最近的 </div>
		if idx := strings.LastIndex(parts[i-1], `<div class="result`); idx >= 0 {
			// 取 result div 开始到适当结束
			endIdx := strings.Index(part, `</div></div></div>`)
			if endIdx < 0 {
				endIdx = strings.Index(part, `</a></div></div>`)
			}
			if endIdx >= 0 {
				blocks = append(blocks, parts[i-1][idx:]+`<a rel="nofollow" class="result__a"`+part[:endIdx+len(`</div></div></div>`)])
			}
		}
	}
	return blocks
}

// extractTitleAndURL 备用标题和 URL 提取
func extractTitleAndURL(block string) (title, link string) {
	// 查找任何 href 和其文本
	linkRe := regexp.MustCompile(`href="([^"]*)"[^>]*>([^<]*)</a>`)
	m := linkRe.FindStringSubmatch(block)
	if m != nil && len(m) >= 3 {
		link = cleanDDGURL(m[1])
		title = cleanHTMLText(m[2])
	}
	return
}

// cleanDDGURL 清理 DuckDuckGo 的 URL（解码重定向 URL）
func cleanDDGURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = htmlEntityDecode(rawURL)

	// DuckDuckGo 使用重定向 URL 格式: //duckduckgo.com/l/?uddg=ENCODED_URL&...
	if strings.Contains(rawURL, "duckduckgo.com/l/?") {
		// 提取 uddg 参数
		if idx := strings.Index(rawURL, "uddg="); idx >= 0 {
			encodedURL := rawURL[idx+5:]
			if end := strings.IndexAny(encodedURL, "&'\""); end > 0 {
				encodedURL = encodedURL[:end]
			}
			decoded, err := url.QueryUnescape(encodedURL)
			if err == nil && (strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://")) {
				return decoded
			}
		}
	}

	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}

	return rawURL
}

// cleanHTMLText 清理 HTML 标签和实体
func cleanHTMLText(text string) string {
	text = htmlTagRe.ReplaceAllString(text, "")
	text = htmlEntityDecode(text)
	text = strings.TrimSpace(text)
	return text
}

// htmlEntityDecode 解码常见 HTML 实体
func htmlEntityDecode(text string) string {
	replacements := map[string]string{
		"&amp;":  "&",
		"&lt;":   "<",
		"&gt;":   ">",
		"&quot;": "\"",
		"&#x27;": "'",
		"&apos;": "'",
		"&#39;":  "'",
	}
	for entity, replacement := range replacements {
		text = strings.ReplaceAll(text, entity, replacement)
	}
	// 数字实体 &#NNN;
	numEntityRe := regexp.MustCompile(`&#(\d+);`)
	text = numEntityRe.ReplaceAllString(text, "")
	return text
}

// isDuplicate 检查是否已存在相同 URL 的结果
func isDuplicate(results []searchResult, r searchResult) bool {
	for _, existing := range results {
		if existing.url == r.url {
			return true
		}
	}
	return false
}
