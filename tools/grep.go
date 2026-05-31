package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"lite-agent/agent"
)

// ============================================================================
// GrepTool — 纯 Go 实现的代码搜索工具（参考 Claude Code GrepTool 设计）
// 零外部依赖，跨平台可用，基于 filepath.Walk + regexp
// ============================================================================

// VCS 目录和常见忽略目录
var grepExcludeDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, ".bzr": true, ".jj": true, ".sl": true,
	"node_modules": true, "__pycache__": true, ".DS_Store": true,
}

// 二进制文件扩展名（跳过不搜索）
var binaryExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
	".obj": true, ".o": true, ".a": true, ".lib": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".bmp": true,
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
	".pdf": true, ".class": true, ".pyc": true, ".pyo": true,
}

// 最大搜索文件大小（1MB），超过此大小的文件跳过
const maxGrepFileSize = 1 * 1024 * 1024

// 最大输出总行数/content长度，防止上下文膨胀
const maxGrepOutputLines = 10000
const maxGrepOutputChars = 20000

// GrepTool 代码搜索工具
type GrepTool struct{}

// NewGrepTool 创建 GrepTool 实例
func NewGrepTool() *GrepTool {
	return &GrepTool{}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return `强大的代码搜索工具（纯Go实现，无需外部依赖，跨平台可用）

用法：
- 使用此工具搜索文件内容，支持完整正则表达式语法
- 支持 glob 参数过滤文件（如 "*.go", "*.{ts,tsx}"）
- 支持 type 参数按扩展名搜索（如 "go", "py", "js"）
- 三种输出模式：content（显示匹配行）、files_with_matches（仅文件路径，默认）、count（显示匹配计数）
- 自动排除 .git/node_modules 等常见非代码目录
- 支持 head_limit 和 offset 实现结果分页（默认 250 条）
- 使用 multiline: true 进行跨行匹配

重要：ALWAYS 使用此 grep 工具进行文件内容搜索，NEVER 通过 shell 调用 grep/rg 命令。`
}

func (t *GrepTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "要搜索的正则表达式模式",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "要搜索的文件或目录。默认为当前工作目录。",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Glob 模式过滤文件（如 '*.go', '*.{ts,tsx}'），多个模式用空格或逗号分隔",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "按扩展名搜索：go, py, js, ts, rs, java, rb, php, c, cpp, h, md, json, yaml, toml, html, css, sql 等",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "输出模式：'content' 显示匹配行，'files_with_matches' 仅显示文件路径（默认），'count' 显示每个文件的匹配数",
			},
			"-B": map[string]interface{}{
				"type":        "integer",
				"description": "显示匹配行之前的行数（需要 output_mode: 'content'）",
			},
			"-A": map[string]interface{}{
				"type":        "integer",
				"description": "显示匹配行之后的行数（需要 output_mode: 'content'）",
			},
			"-C": map[string]interface{}{
				"type":        "integer",
				"description": "显示匹配行前后的行数（需要 output_mode: 'content'）",
			},
			"-n": map[string]interface{}{
				"type":        "boolean",
				"description": "显示行号（仅在 output_mode: 'content' 时有效），默认 true",
			},
			"-i": map[string]interface{}{
				"type":        "boolean",
				"description": "忽略大小写",
			},
			"head_limit": map[string]interface{}{
				"type":        "integer",
				"description": "限制输出条数，类似 '| head -N'。默认 250，传 0 表示无限制。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "跳过前 N 条结果，类似 '| tail -n +N'。默认 0。",
			},
			"multiline": map[string]interface{}{
				"type":        "boolean",
				"description": "启用多行模式（跨行匹配）。默认 false。注意：多行模式下需要将整个文件读入内存。",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如：搜索包含 'HandleHTTP' 的文件",
			},
		},
		"required": []string{"pattern", "intent"},
	}
}

// grepMatch 单条匹配结果
type grepMatch struct {
	file    string // 文件路径（相对路径）
	lineNum int    // 行号（1-based）
	content string // 匹配行内容
}

// grepFileResult 单个文件的搜索结果
type grepFileResult struct {
	file    string
	matches []grepMatch
	count   int
	err     error
}

func (t *GrepTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
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

	// 验证搜索路径存在
	absPath, err := filepath.Abs(searchPath)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("路径无效: %s", searchPath)), IsError: true}, nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return &agent.ToolResult{Content: agent.FormatValidationError(fmt.Sprintf("路径不存在: %s", searchPath)), IsError: true}, nil
	}

	globStr, _ := args["glob"].(string)
	fileType, _ := args["type"].(string)
	outputMode, _ := args["output_mode"].(string)
	if outputMode == "" {
		outputMode = "files_with_matches"
	}

	contextBefore := getIntArg(args, "-B")
	contextAfter := getIntArg(args, "-A")
	contextAround := getIntArg(args, "-C")
	showLineNumbers := true
	if v, ok := args["-n"].(bool); ok {
		showLineNumbers = v
	}
	caseInsensitive := false
	if v, ok := args["-i"].(bool); ok {
		caseInsensitive = v
	}
	headLimit := 250
	if v, ok := args["head_limit"].(float64); ok {
		headLimit = int(v)
	}
	offset := 0
	if v, ok := args["offset"].(float64); ok {
		offset = int(v)
	}
	multiline := false
	if v, ok := args["multiline"].(bool); ok {
		multiline = v
	}

	// 2. 编译正则
	reFlags := ""
	if caseInsensitive {
		reFlags = "(?i)"
	}
	if multiline {
		// 多行模式下 . 匹配换行，^/$ 匹配行首尾
		reFlags += "(?s)"
	}
	re, err := regexp.Compile(reFlags + pattern)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatValidationError(fmt.Sprintf("正则表达式无效: %v", err)),
			IsError: true,
		}, nil
	}

	// 3. 构建 glob 过滤器
	var globPatterns []string
	if globStr != "" {
		globPatterns = parseGlobPatterns(globStr)
	}
	if fileType != "" {
		globPatterns = append(globPatterns, "*."+fileType)
	}

	// 4. 确定上下文行数
	ctxBefore := contextBefore
	ctxAfter := contextAfter
	if contextAround > 0 {
		ctxBefore = contextAround
		ctxAfter = contextAround
	}

	// 5. 搜索
	var results []grepFileResult
	if info.IsDir() {
		results = t.searchDir(ctx, absPath, re, globPatterns, multiline)
	} else {
		results = t.searchFile(absPath, re, multiline)
	}

	// 6. 格式化输出
	switch outputMode {
	case "content":
		return t.formatContent(results, showLineNumbers, ctxBefore, ctxAfter, headLimit, offset)
	case "count":
		return t.formatCount(results, headLimit, offset)
	default:
		return t.formatFilesWithMatches(results, headLimit, offset)
	}
}

// searchDir 递归搜索目录
func (t *GrepTool) searchDir(ctx context.Context, root string, re *regexp.Regexp, globPatterns []string, multiline bool) []grepFileResult {
	var files []string

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			return nil // 跳过无法访问的文件/目录
		}

		// 跳过目录
		if info.IsDir() {
			base := info.Name()
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

		// 跳过大文件
		if info.Size() > maxGrepFileSize {
			return nil
		}

		// glob 过滤
		if len(globPatterns) > 0 {
			relPath, _ := filepath.Rel(root, path)
			if !matchAnyGlob(relPath, globPatterns) {
				return nil
			}
		}

		// 跳过符号链接指向目录（非文件）
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := os.Stat(path)
			if err != nil || resolved.IsDir() {
				return nil
			}
		}

		files = append(files, path)
		return nil
	})

	// 并发搜索文件
	return t.searchFilesConcurrently(ctx, root, files, re, multiline)
}

// searchFilesConcurrently 并发搜索文件列表
func (t *GrepTool) searchFilesConcurrently(ctx context.Context, root string, files []string, re *regexp.Regexp, multiline bool) []grepFileResult {
	if len(files) == 0 {
		return nil
	}

	// 限制并发数
	maxWorkers := 8
	if len(files) < maxWorkers {
		maxWorkers = len(files)
	}

	type job struct {
		idx  int
		path string
	}

	jobs := make(chan job, len(files))
	results := make([]grepFileResult, len(files))
	var processed atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < maxWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
				}
				r := t.searchFile(j.path, re, multiline)
				if len(r) > 0 {
					results[j.idx] = r[0]
				}
				processed.Add(1)
			}
		}()
	}

	for i, f := range files {
		jobs <- job{idx: i, path: f}
	}
	close(jobs)
	wg.Wait()

	// 过滤空结果
	var finalResults []grepFileResult
	for _, r := range results {
		if r.file != "" && (r.count > 0 || len(r.matches) > 0) {
			finalResults = append(finalResults, r)
		}
	}

	// 按文件修改时间降序排列
	sort.Slice(finalResults, func(i, j int) bool {
		infoI, errI := os.Stat(finalResults[i].file)
		infoJ, errJ := os.Stat(finalResults[j].file)
		if errI != nil || errJ != nil {
			return finalResults[i].file < finalResults[j].file
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	// 转相对路径
	for i := range finalResults {
		rel, err := filepath.Rel(root, finalResults[i].file)
		if err == nil {
			finalResults[i].file = rel
		}
	}

	return finalResults
}

// searchFile 搜索单个文件
func (t *GrepTool) searchFile(filePath string, re *regexp.Regexp, multiline bool) []grepFileResult {
	if multiline {
		return t.searchFileMultiline(filePath, re)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	if info.Size() > maxGrepFileSize {
		return nil
	}

	var matches []grepMatch
	scanner := bufio.NewScanner(f)
	// 增加缓冲区以处理长行
	scanner.Buffer(make([]byte, 1024*1024), maxGrepFileSize)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			matches = append(matches, grepMatch{
				lineNum: lineNum,
				content: line,
			})
		}
	}

	if len(matches) == 0 {
		return nil
	}

	return []grepFileResult{{
		file:    filePath,
		matches: matches,
		count:   len(matches),
	}}
}

// searchFileMultiline 多行模式搜索
func (t *GrepTool) searchFileMultiline(filePath string, re *regexp.Regexp) []grepFileResult {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	if len(content) > maxGrepFileSize {
		return nil
	}

	// 检查是否为二进制文件
	if isBinaryContent(content) {
		return nil
	}

	text := string(content)
	allMatches := re.FindAllStringIndex(text, -1)
	if len(allMatches) == 0 {
		return nil
	}

	// 计算行号
	lineStarts := getLineStarts(text)
	var matches []grepMatch
	for _, m := range allMatches {
		lineNum := findLineNumber(lineStarts, m[0])
		matchedText := text[m[0]:m[1]]
		// 截断太长的匹配内容
		if len(matchedText) > 500 {
			matchedText = matchedText[:500] + "..."
		}
		matches = append(matches, grepMatch{
			lineNum: lineNum,
			content: matchedText,
		})
	}

	return []grepFileResult{{
		file:    filePath,
		matches: matches,
		count:   len(matches),
	}}
}

// isBinaryContent 检测内容是否为二进制
func isBinaryContent(data []byte) bool {
	checkLen := len(data)
	if checkLen > 8000 {
		checkLen = 8000
	}
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// getLineStarts 获取每行的起始位置
func getLineStarts(text string) []int {
	starts := []int{0}
	for i, c := range text {
		if c == '\n' && i+1 < len(text) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// findLineNumber 根据字节偏移查找行号（1-based）
func findLineNumber(starts []int, offset int) int {
	// 二分查找
	lo, hi := 0, len(starts)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if starts[mid] <= offset {
			if mid == len(starts)-1 || starts[mid+1] > offset {
				return mid + 1
			}
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return 1
}

// ============================================================================
// 输出格式化
// ============================================================================

// formatContent 格式化 content 模式输出
func (t *GrepTool) formatContent(results []grepFileResult, showLineNumbers bool, ctxBefore, ctxAfter, headLimit, offset int) (*agent.ToolResult, error) {
	var lines []string
	totalLines := 0

	// 应用 offset
	lineIdx := 0
	for _, fr := range results {
		for _, m := range fr.matches {
			if lineIdx >= offset {
				var line string
				if showLineNumbers {
					line = fmt.Sprintf("%s:%d:%s", fr.file, m.lineNum, m.content)
				} else {
					line = fmt.Sprintf("%s:%s", fr.file, m.content)
				}
				lines = append(lines, line)
				totalLines++
				if headLimit > 0 && totalLines >= headLimit {
					goto done
				}
			}
			lineIdx++
		}
	}
done:

	content := strings.Join(lines, "\n")
	if content == "" {
		content = "没有找到匹配结果"
	}

	// 截断过长的输出
	applyLimit := false
	if headLimit > 0 && totalLines >= headLimit {
		applyLimit = true
	}
	if len(content) > maxGrepOutputChars {
		content = content[:maxGrepOutputChars] + "\n... (输出被截断)"
	}

	result := content
	if applyLimit {
		result += fmt.Sprintf("\n\n[使用 pagination: limit=%d, offset=%d]", headLimit, offset)
	}

	return &agent.ToolResult{
		Content: result,
		RichData: map[string]interface{}{
			"mode":     "content",
			"numFiles": len(results),
			"numLines": totalLines,
		},
	}, nil
}

// formatCount 格式化 count 模式输出
func (t *GrepTool) formatCount(results []grepFileResult, headLimit, offset int) (*agent.ToolResult, error) {
	// 按匹配数降序排列
	sort.Slice(results, func(i, j int) bool {
		return results[i].count > results[j].count
	})

	totalMatches := 0
	var lines []string

	start := offset
	end := len(results)
	if headLimit > 0 && start+headLimit < end {
		end = start + headLimit
	}

	for i := start; i < end; i++ {
		r := results[i]
		lines = append(lines, fmt.Sprintf("%s:%d", r.file, r.count))
		totalMatches += r.count
	}

	content := strings.Join(lines, "\n")
	appliedLimit := headLimit > 0 && end < len(results)

	summary := fmt.Sprintf("\n\n在 %d 个文件中找到 %d 处匹配", len(results), totalMatches)
	if appliedLimit {
		summary += fmt.Sprintf("（显示前 %d 条，offset=%d）", headLimit, offset)
	}

	return &agent.ToolResult{
		Content: content + summary,
		RichData: map[string]interface{}{
			"mode":       "count",
			"numFiles":   len(results),
			"numMatches": totalMatches,
		},
	}, nil
}

// formatFilesWithMatches 格式化 files_with_matches 模式输出
func (t *GrepTool) formatFilesWithMatches(results []grepFileResult, headLimit, offset int) (*agent.ToolResult, error) {
	start := offset
	end := len(results)
	if headLimit > 0 && start+headLimit < end {
		end = start + headLimit
	}
	if start >= len(results) {
		start = len(results)
	}

	displayed := results[start:end]
	var filenames []string
	for _, r := range displayed {
		filenames = append(filenames, r.file)
	}

	appliedLimit := headLimit > 0 && end < len(results)

	if len(filenames) == 0 {
		return &agent.ToolResult{Content: "没有找到匹配的文件"}, nil
	}

	limitInfo := ""
	if appliedLimit {
		limitInfo = fmt.Sprintf("（limit=%d, offset=%d）", headLimit, offset)
	}

	result := fmt.Sprintf("找到 %d 个文件%s\n%s", len(results), limitInfo, strings.Join(filenames, "\n"))

	return &agent.ToolResult{
		Content: result,
		RichData: map[string]interface{}{
			"mode":     "files_with_matches",
			"numFiles": len(results),
			"filenames": filenames,
		},
	}, nil
}

// ============================================================================
// 辅助函数
// ============================================================================

// parseGlobPatterns 解析 glob 模式字符串
// 支持: "*.go *.ts" 或 "*.{ts,tsx}" 或 "*.go,*.ts"
func parseGlobPatterns(s string) []string {
	var patterns []string
	// 先按空白分割
	rawPatterns := strings.Fields(s)
	for _, raw := range rawPatterns {
		// 去掉尾部的逗号
		raw = strings.TrimSuffix(raw, ",")
		// 如果包含 {braces}，展开
		if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
			expanded := expandBracePattern(raw)
			patterns = append(patterns, expanded...)
		} else {
			// 按逗号分割
			for _, p := range strings.Split(raw, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					patterns = append(patterns, p)
				}
			}
		}
	}
	return patterns
}

// expandBracePattern 展开 {a,b} 模式
// 如 "*.{go,ts}" → ["*.go", "*.ts"]
func expandBracePattern(pattern string) []string {
	// 查找 { 和 }
	braceStart := strings.Index(pattern, "{")
	braceEnd := strings.Index(pattern, "}")
	if braceStart < 0 || braceEnd < 0 || braceEnd <= braceStart {
		return []string{pattern}
	}

	prefix := pattern[:braceStart]
	suffix := pattern[braceEnd+1:]
	inside := pattern[braceStart+1 : braceEnd]

	var result []string
	for _, opt := range strings.Split(inside, ",") {
		opt = strings.TrimSpace(opt)
		result = append(result, prefix+opt+suffix)
	}
	return result
}

// matchAnyGlob 检查路径是否匹配任一 glob 模式
func matchAnyGlob(path string, patterns []string) bool {
	for _, p := range patterns {
		matched, err := filepath.Match(p, filepath.Base(path))
		if err == nil && matched {
			return true
		}
		// 也尝试匹配完整相对路径
		matched, err = filepath.Match(p, path)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// getIntArg 安全获取整数参数
func getIntArg(args map[string]interface{}, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}
