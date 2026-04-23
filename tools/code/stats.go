// file_stats.go
package code

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

type LanguageConfig struct {
	Extensions    []string
	LineComment   []string
	BlockComment  [][2]string
}

var languageConfigs = map[string]LanguageConfig{
	"Go": {
		Extensions:   []string{".go"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Python": {
		Extensions:   []string{".py", ".pyw"},
		LineComment:  []string{"#"},
		BlockComment: [][2]string{{`"""`, `"""`}, {"'''", "'''"}},
	},
	"JavaScript": {
		Extensions:   []string{".js", ".jsx", ".mjs"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"TypeScript": {
		Extensions:   []string{".ts", ".tsx"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Java": {
		Extensions:   []string{".java"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"C": {
		Extensions:   []string{".c", ".h"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"C++": {
		Extensions:   []string{".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"C#": {
		Extensions:   []string{".cs"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Ruby": {
		Extensions:   []string{".rb"},
		LineComment:  []string{"#"},
		BlockComment: [][2]string{{"=begin", "=end"}},
	},
	"PHP": {
		Extensions:   []string{".php"},
		LineComment:  []string{"//", "#"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Swift": {
		Extensions:   []string{".swift"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Kotlin": {
		Extensions:   []string{".kt", ".kts"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Rust": {
		Extensions:   []string{".rs"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"HTML": {
		Extensions:   []string{".html", ".htm"},
		LineComment:  []string{},
		BlockComment: [][2]string{{"<!--", "-->"}},
	},
	"CSS": {
		Extensions:   []string{".css", ".scss", ".sass"},
		LineComment:  []string{"//"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"SQL": {
		Extensions:   []string{".sql"},
		LineComment:  []string{"--"},
		BlockComment: [][2]string{{"/*", "*/"}},
	},
	"Shell": {
		Extensions:   []string{".sh", ".bash", ".zsh"},
		LineComment:  []string{"#"},
		BlockComment: [][2]string{},
	},
	"YAML": {
		Extensions:   []string{".yaml", ".yml"},
		LineComment:  []string{"#"},
		BlockComment: [][2]string{},
	},
	"Markdown": {
		Extensions:   []string{".md"},
		LineComment:  []string{},
		BlockComment: [][2]string{},
	},
	"JSON": {
		Extensions:   []string{".json"},
		LineComment:  []string{},
		BlockComment: [][2]string{},
	},
}

type FileStats struct {
	Path        string `json:"path"`
	Language    string `json:"language"`
	CodeLines   int    `json:"code_lines"`
	CommentLines int   `json:"comment_lines"`
	EmptyLines  int    `json:"empty_lines"`
	TotalLines  int    `json:"total_lines"`
}

type Summary struct {
	TotalFiles     int            `json:"total_files"`
	TotalCodeLines int            `json:"total_code_lines"`
	TotalCommentLines int         `json:"total_comment_lines"`
	TotalEmptyLines  int          `json:"total_empty_lines"`
	ByLanguage       map[string]LanguageSummary `json:"by_language"`
	ElapsedTime      string        `json:"elapsed_time"`
}

type LanguageSummary struct {
	Files       int `json:"files"`
	CodeLines   int `json:"code_lines"`
	CommentLines int `json:"comment_lines"`
	EmptyLines  int `json:"empty_lines"`
}

type Analyzer struct {
	ignoreMatcher gitignore.Matcher
	excludeDirs   map[string]bool
	workers       int
	fileChan      chan string
	results       chan FileStats
	wg            sync.WaitGroup
	stats         *Summary
	mu            sync.Mutex
}

func NewAnalyzer(ignoreFile string, excludeDirs []string, workers int) (*Analyzer, error) {
	analyzer := &Analyzer{
		excludeDirs: make(map[string]bool),
		workers:     workers,
		fileChan:    make(chan string, 1000),
		results:     make(chan FileStats, 1000),
		stats: &Summary{
			ByLanguage: make(map[string]LanguageSummary),
		},
	}

	// 设置排除目录
	for _, dir := range excludeDirs {
		analyzer.excludeDirs[dir] = true
	}

	// 加载 .gitignore
	if ignoreFile != "" {
		patterns, err := loadGitignore(ignoreFile)
		if err == nil {
			analyzer.ignoreMatcher = gitignore.NewMatcher(patterns)
		}
	}

	return analyzer, nil
}

func loadGitignore(path string) ([]gitignore.Pattern, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, gitignore.ParsePattern(line, nil))
		}
	}
	return patterns, scanner.Err()
}

func (a *Analyzer) shouldIgnore(path string) bool {
	// 检查排除目录
	dir := filepath.Dir(path)
	for excludeDir := range a.excludeDirs {
		if strings.Contains(dir, excludeDir) || strings.Contains(path, excludeDir) {
			return true
		}
	}

	// 检查 .gitignore
	if a.ignoreMatcher != nil {
		relPath, _ := filepath.Rel(".", path)
		return a.ignoreMatcher.Match(strings.Split(relPath, string(filepath.Separator)), false)
	}
	return false
}

func (a *Analyzer) analyzeFile(path string) *FileStats {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(path))
	language := detectLanguage(ext)
	
	stats := &FileStats{
		Path:     path,
		Language: language,
	}

	scanner := bufio.NewScanner(file)
	inBlockComment := false
	currentBlockStart := ""

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		
		stats.TotalLines++
		
		if trimmed == "" {
			stats.EmptyLines++
			continue
		}

		// 多行注释检测
		if !inBlockComment {
			// 检查是否是多行注释开始
			blockStart, blockEnd := findBlockCommentStart(trimmed, language)
			if blockStart != "" {
				inBlockComment = true
				currentBlockStart = blockStart
				// 检查是否在同一行结束
				if strings.Contains(trimmed, blockEnd) {
					inBlockComment = false
					stats.CommentLines++
					continue
				}
				stats.CommentLines++
				continue
			}
			
			// 单行注释检测
			if isLineComment(trimmed, language) {
				stats.CommentLines++
				continue
			}
		} else {
			// 在多行注释中
			stats.CommentLines++
			if strings.Contains(trimmed, getBlockEnd(currentBlockStart, language)) {
				inBlockComment = false
			}
			continue
		}
		
		stats.CodeLines++
	}

	return stats
}

func detectLanguage(ext string) string {
	for lang, config := range languageConfigs {
		for _, e := range config.Extensions {
			if e == ext {
				return lang
			}
		}
	}
	return "Other"
}

func findBlockCommentStart(line, language string) (string, string) {
	config, exists := languageConfigs[language]
	if !exists {
		return "", ""
	}
	
	for _, block := range config.BlockComment {
		if strings.Contains(line, block[0]) {
			return block[0], block[1]
		}
	}
	return "", ""
}

func getBlockEnd(start, language string) string {
	config, exists := languageConfigs[language]
	if !exists {
		return ""
	}
	
	for _, block := range config.BlockComment {
		if block[0] == start {
			return block[1]
		}
	}
	return ""
}

func isLineComment(line, language string) bool {
	config, exists := languageConfigs[language]
	if !exists {
		return false
	}
	
	for _, comment := range config.LineComment {
		if strings.HasPrefix(line, comment) {
			return true
		}
	}
	return false
}

func (a *Analyzer) worker() {
	defer a.wg.Done()
	for path := range a.fileChan {
		if stats := a.analyzeFile(path); stats != nil {
			a.results <- *stats
		}
	}
}

func (a *Analyzer) collectResults() {
	var totalCode, totalComment, totalEmpty int64
	var totalFiles int64
	
	for stats := range a.results {
		atomic.AddInt64(&totalFiles, 1)
		atomic.AddInt64(&totalCode, int64(stats.CodeLines))
		atomic.AddInt64(&totalComment, int64(stats.CommentLines))
		atomic.AddInt64(&totalEmpty, int64(stats.EmptyLines))
		
		a.mu.Lock()
		langSum := a.stats.ByLanguage[stats.Language]
		langSum.Files++
		langSum.CodeLines += stats.CodeLines
		langSum.CommentLines += stats.CommentLines
		langSum.EmptyLines += stats.EmptyLines
		a.stats.ByLanguage[stats.Language] = langSum
		a.mu.Unlock()
	}
	
	a.stats.TotalFiles = int(totalFiles)
	a.stats.TotalCodeLines = int(totalCode)
	a.stats.TotalCommentLines = int(totalComment)
	a.stats.TotalEmptyLines = int(totalEmpty)
}

func (a *Analyzer) walkDirectory(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		
		if info.IsDir() {
			if a.shouldIgnore(path) {
				return filepath.SkipDir
			}
			return nil
		}
		
		if !a.shouldIgnore(path) {
			a.fileChan <- path
		}
		return nil
	})
}

func (a *Analyzer) Run(root string) error {
	// 启动 workers
	for i := 0; i < a.workers; i++ {
		a.wg.Add(1)
		go a.worker()
	}
	
	// 启动结果收集器
	go a.collectResults()
	
	// 遍历目录
	err := a.walkDirectory(root)
	
	close(a.fileChan)
	a.wg.Wait()
	close(a.results)
	
	return err
}

func (a *Analyzer) PrintSummary(outputFormat, outputFile string) {
	a.stats.ElapsedTime = "N/A"
	
	switch outputFormat {
	case "json":
		output, _ := json.MarshalIndent(a.stats, "", "  ")
		if outputFile != "" {
			os.WriteFile(outputFile, output, 0644)
		} else {
			fmt.Println(string(output))
		}
		
	case "csv":
		writer := csv.NewWriter(os.Stdout)
		if outputFile != "" {
			f, _ := os.Create(outputFile)
			defer f.Close()
			writer = csv.NewWriter(f)
		}
		
		writer.Write([]string{"Language", "Files", "Code Lines", "Comment Lines", "Empty Lines"})
		for lang, sum := range a.stats.ByLanguage {
			writer.Write([]string{
				lang,
				fmt.Sprintf("%d", sum.Files),
				fmt.Sprintf("%d", sum.CodeLines),
				fmt.Sprintf("%d", sum.CommentLines),
				fmt.Sprintf("%d", sum.EmptyLines),
			})
		}
		writer.Flush()
		
	default: // table format
		fmt.Printf("\n📊 代码统计结果\n")
		fmt.Printf("=================================\n")
		fmt.Printf("总文件数:    %d\n", a.stats.TotalFiles)
		fmt.Printf("总代码行数:  %d\n", a.stats.TotalCodeLines)
		fmt.Printf("总注释行数:  %d\n", a.stats.TotalCommentLines)
		fmt.Printf("总空行数:    %d\n", a.stats.TotalEmptyLines)
		fmt.Printf("\n按语言类型:\n")
		
		// 排序输出
		langs := make([]string, 0, len(a.stats.ByLanguage))
		for lang := range a.stats.ByLanguage {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		
		for _, lang := range langs {
			sum := a.stats.ByLanguage[lang]
			fmt.Printf("  %-12s %3d个  %5d行  (注释:%-4d 空行:%-4d)\n",
				lang, sum.Files, sum.CodeLines, sum.CommentLines, sum.EmptyLines)
		}
	}
	
	// 获取 Summary 结果用于工具返回（不打印到 stdout）
	_ = outputFormat
	_ = outputFile
}

// GetCodeStats 导出函数：对指定路径进行代码统计，返回 JSON 格式的统计结果
func GetCodeStats(rootPath string, workers int, excludeStr string) ([]byte, error) {
	excludeDirs := strings.Split(excludeStr, ",")
	analyzer, err := NewAnalyzer(".gitignore", excludeDirs, workers)
	if err != nil {
		return nil, fmt.Errorf("初始化失败: %v", err)
	}
	
	startTime := time.Now()
	err = analyzer.Run(rootPath)
	elapsed := time.Since(startTime)
	
	if err != nil {
		return nil, fmt.Errorf("统计失败: %v", err)
	}
	
	analyzer.stats.ElapsedTime = elapsed.String()
	return json.MarshalIndent(analyzer.stats, "", "  ")
}