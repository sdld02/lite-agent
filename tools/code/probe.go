package code

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type OutputMode string

const (
	ModeSummary   OutputMode = "summary"   // 仅统计信息
	ModeStructure OutputMode = "structure" // JSON 目录树结构（可限制数量）
	ModeFlat      OutputMode = "flat"      // 扁平列表（适合 grep）
	ModeGrouped   OutputMode = "grouped"   // 按扩展名分组
	ModeTree      OutputMode = "tree"      // 文本树形可视化（类似 tree 命令）
)

// TreeConfig 文本树形可视化配置
type TreeConfig struct {
	RootPath   string
	MaxDepth   int
	ShowFiles  bool
	IgnoreDirs []string
}

// TextTree 文本树形输出结果
type TextTree struct {
	Tree     string `json:"tree"`
	Root     string `json:"root"`
	TotalDirs int   `json:"totalDirs"`
	TotalFiles int  `json:"totalFiles"`
}

type ProjectStats struct {
	TotalDirs   int            `json:"totalDirs"`
	TotalFiles  int            `json:"totalFiles"`
	TotalSize   int64          `json:"totalSize"`
	FileTypes   map[string]int `json:"fileTypes"`   // 扩展名 -> 数量
	MaxDepth    int            `json:"maxDepth"`
	TopLevelDirs []string      `json:"topLevelDirs"`
}

type FileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"isDir"`
	Size     int64       `json:"size,omitempty"`      // 文件大小（字节），仅文件有效
	Ext      string      `json:"ext,omitempty"`       // 文件扩展名，仅文件有效
	Children []*FileNode `json:"children,omitempty"`  // 子节点，仅目录有效
	Truncated bool        `json:"truncated"`     // 是否被截断
}

// 1. 摘要模式 - 最小 token 消耗
func GetProjectSummary(rootPath string, maxDepth int, ignoreDirs []string) ([]byte, error) {
	if ignoreDirs == nil {
		ignoreDirs = []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"}
	}

	stats := &ProjectStats{
		FileTypes: make(map[string]int),
	}
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == rootPath {
			return err
		}
		
		// 跳过被忽略的目录
		if d.IsDir() && contains(ignoreDirs, d.Name()) {
			return filepath.SkipDir
		}
		
		relPath, _ := filepath.Rel(rootPath, path)
		depth := strings.Count(relPath, string(filepath.Separator))
		
		if depth > maxDepth {
			return filepath.SkipDir
		}
		
		if d.IsDir() {
			stats.TotalDirs++
			if depth == 1 {
				stats.TopLevelDirs = append(stats.TopLevelDirs, d.Name())
			}
		} else {
			stats.TotalFiles++
			
			// 获取文件大小
			info, _ := d.Info()
			if info != nil {
				stats.TotalSize += info.Size()
			}
			
			// 统计文件类型
			ext := strings.TrimPrefix(filepath.Ext(d.Name()), ".")
			if ext == "" {
				ext = "no_ext"
			}
			stats.FileTypes[ext]++
		}
		return nil
	})
	
	if err != nil {
		return nil, err
	}
	
	// 计算最大深度
	stats.MaxDepth = calculateMaxDepth(rootPath, maxDepth)
	
	return json.MarshalIndent(stats, "", "  ")
}

// 2. 智能截断模式 - 只显示重要部分
type TruncatedTree struct {
	Root      string      `json:"root"`
	TotalDirs int         `json:"totalDirs"`
	TotalFiles int        `json:"totalFiles"`
	Children  []*FileNode `json:"children"`
	Truncated bool        `json:"truncated"`     // 是否被截断
	HiddenCount int       `json:"hiddenCount"`   // 隐藏的项目数
}

func GetSmartTree(rootPath string, maxDepth int, maxItemsPerDir int, ignoreDirs []string) ([]byte, error) {
	if ignoreDirs == nil {
		ignoreDirs = []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"}
	}

	absPath, _ := filepath.Abs(rootPath)
	root := &FileNode{
		Name:  filepath.Base(absPath),
		Path:  absPath,
		IsDir: true,
	}
	
	totalDirs, totalFiles := 0, 0
	buildSmartTree(absPath, root, 0, maxDepth, maxItemsPerDir, ignoreDirs, &totalDirs, &totalFiles)
	
	result := &TruncatedTree{
		Root:       root.Name,
		TotalDirs:  totalDirs,
		TotalFiles: totalFiles,
		Children:   root.Children,
		Truncated:  len(root.Children) > maxItemsPerDir,
		HiddenCount: max(0, len(root.Children)-maxItemsPerDir),
	}
	
	return json.MarshalIndent(result, "", "  ")
}

func buildSmartTree(path string, node *FileNode, depth, maxDepth, maxItems int, ignoreDirs []string, totalDirs, totalFiles *int) {
	if depth >= maxDepth {
		return
	}
	
	entries, _ := os.ReadDir(path)
	
	// 过滤忽略目录
	filtered := []fs.DirEntry{}
	for _, entry := range entries {
		if entry.IsDir() && contains(ignoreDirs, entry.Name()) {
			continue
		}
		filtered = append(filtered, entry)
	}
	
	// 限制每层显示数量
	if len(filtered) > maxItems {
		filtered = filtered[:maxItems]
		node.Truncated = true
	}
	
	for _, entry := range filtered {
		if entry.IsDir() {
			*totalDirs++
			child := &FileNode{
				Name:  entry.Name(),
				Path:  filepath.Join(node.Path, entry.Name()),
				IsDir: true,
			}
			buildSmartTree(filepath.Join(path, entry.Name()), child, depth+1, maxDepth, maxItems, ignoreDirs, totalDirs, totalFiles)
			node.Children = append(node.Children, child)
		} else {
			*totalFiles++
			node.Children = append(node.Children, &FileNode{
				Name:  entry.Name(),
				Path:  filepath.Join(node.Path, entry.Name()),
				IsDir: false,
			})
		}
	}
}

// 3. 扁平模式 - 推送完整路径列表
func GetFlatList(rootPath string, maxDepth int, maxItems int, ignoreDirs []string) ([]byte, error) {
	if ignoreDirs == nil {
		ignoreDirs = []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"}
	}

	var files []string
	var dirs []string
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		
		// 跳过被忽略的目录
		if d.IsDir() && contains(ignoreDirs, d.Name()) {
			return filepath.SkipDir
		}
		
		relPath, _ := filepath.Rel(rootPath, path)
		if relPath == "." {
			return nil
		}
		
		depth := strings.Count(relPath, string(filepath.Separator))
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		
		if d.IsDir() {
			dirs = append(dirs, relPath)
		} else {
			files = append(files, relPath)
		}
		
		// 限制总数
		if len(files)+len(dirs) >= maxItems {
			return filepath.SkipAll
		}
		
		return nil
	})
	
	if err != nil && err != filepath.SkipAll {
		return nil, err
	}
	
	result := map[string]interface{}{
		"total": len(files) + len(dirs),
		"dirs":  dirs,
		"files": files,
	}
	
	// 标记是否截断
	if len(files)+len(dirs) >= maxItems {
		result["truncated"] = true
		result["limit"] = maxItems
	}
	
	return json.MarshalIndent(result, "", "  ")
}

// 4. 按类型分组 - 快速了解项目组成
type GroupedStructure struct {
	ByType map[string][]string `json:"byType"`  // 扩展名 -> 文件列表
	Summary map[string]int     `json:"summary"` // 扩展名 -> 数量
}

func GetGroupedByType(rootPath string, maxDepth int, ignoreDirs []string) ([]byte, error) {
	if ignoreDirs == nil {
		ignoreDirs = []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"}
	}

	grouped := &GroupedStructure{
		ByType:  make(map[string][]string),
		Summary: make(map[string]int),
	}
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 跳过被忽略的目录
		if d.IsDir() {
			if contains(ignoreDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(rootPath, path)
		depth := strings.Count(relPath, string(filepath.Separator))
		
		if depth > maxDepth {
			return nil
		}
		
		ext := strings.TrimPrefix(filepath.Ext(d.Name()), ".")
		if ext == "" {
			ext = "no_ext"
		}
		
		// 限制每个类型最多 20 个示例
		if len(grouped.ByType[ext]) < 20 {
			grouped.ByType[ext] = append(grouped.ByType[ext], relPath)
		}
		grouped.Summary[ext]++
		
		return nil
	})

	if err != nil {
		return nil, err
	}
	
	return json.MarshalIndent(grouped, "", "  ")
}

// 5. 差异模式 - 只显示最近修改的文件
func GetRecentFiles(rootPath string, maxDepth int, days int) ([]byte, error) {
	// 实现按修改时间过滤
	// 返回最近 days 天内修改的文件
	return nil, nil
}

// Helper: 计算最大深度
func calculateMaxDepth(rootPath string, limit int) int {
	maxDepth := 0
	filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(rootPath, path)
		depth := strings.Count(relPath, string(filepath.Separator))
		if depth > maxDepth {
			maxDepth = depth
		}
		return nil
	})
	return maxDepth
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// 6. 文本树形可视化模式 - 类似 tree 命令的输出
// GetTextTree 生成可读的文本树形目录结构
func GetTextTree(rootPath string, maxDepth int, showFiles bool, ignoreDirs []string) ([]byte, error) {
	if ignoreDirs == nil {
		ignoreDirs = []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"}
	}

	cfg := TreeConfig{
		RootPath:   rootPath,
		MaxDepth:   maxDepth,
		ShowFiles:  showFiles,
		IgnoreDirs: ignoreDirs,
	}

	textTree, err := generateTree(cfg)
	if err != nil {
		return nil, err
	}

	// 同时统计目录和文件数量
	totalDirs, totalFiles := countTreeItems(cfg)

	result := TextTree{
		Tree:       textTree,
		Root:       filepath.Base(rootPath),
		TotalDirs:  totalDirs,
		TotalFiles: totalFiles,
	}

	return json.MarshalIndent(result, "", "  ")
}

// generateTree 生成文本树形结构
func generateTree(cfg TreeConfig) (string, error) {
	var builder strings.Builder
	absPath, err := filepath.Abs(cfg.RootPath)
	if err != nil {
		return "", err
	}

	builder.WriteString(filepath.Base(absPath) + "/\n")
	walkDir(absPath, "", 0, cfg, &builder)
	return builder.String(), nil
}

// walkDir 递归遍历目录并构建文本树
func walkDir(path, prefix string, depth int, cfg TreeConfig, builder *strings.Builder) {
	if depth >= cfg.MaxDepth {
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}

	// 过滤忽略目录
	filtered := []fs.DirEntry{}
	for _, entry := range entries {
		if entry.IsDir() && contains(cfg.IgnoreDirs, entry.Name()) {
			continue
		}
		filtered = append(filtered, entry)
	}

	// 排序保证输出稳定
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name() < filtered[j].Name()
	})

	for i, entry := range filtered {
		isLast := i == len(filtered)-1
		var connector, childPrefix string

		if isLast {
			connector = "└── "
			childPrefix = prefix + "    "
		} else {
			connector = "├── "
			childPrefix = prefix + "│   "
		}

		if entry.IsDir() {
			builder.WriteString(prefix + connector + entry.Name() + "/\n")
			walkDir(filepath.Join(path, entry.Name()), childPrefix, depth+1, cfg, builder)
		} else if cfg.ShowFiles {
			builder.WriteString(prefix + connector + entry.Name() + "\n")
		}
	}
}

// contains 检查字符串是否在切片中
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// countTreeItems 统计目录和文件数量（与 walkDir 保持一致的过滤逻辑）
func countTreeItems(cfg TreeConfig) (int, int) {
	dirs, files := 0, 0
	filepath.WalkDir(cfg.RootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == cfg.RootPath {
			return nil
		}
		relPath, _ := filepath.Rel(cfg.RootPath, path)
		depth := strings.Count(relPath, string(filepath.Separator))
		if depth >= cfg.MaxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// 忽略指定目录（与 walkDir 中的过滤逻辑一致）
		if d.IsDir() && contains(cfg.IgnoreDirs, d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			dirs++
		} else {
			files++
		}
		return nil
	})
	return dirs, files
}

// GetProjectStructure 快速获取项目结构（便捷函数）
func GetProjectStructure(rootPath string, maxDepth int) (string, error) {
	cfg := TreeConfig{
		RootPath:   rootPath,
		MaxDepth:   maxDepth,
		ShowFiles:  true,
		IgnoreDirs: []string{".git", "node_modules", ".idea", ".vscode", "__pycache__"},
	}
	return generateTree(cfg)
}

// GetProjectTree 以文本树形式返回项目结构（JSON 封装）
func GetProjectTree(rootPath string, maxDepth int) ([]byte, error) {
	return GetTextTree(rootPath, maxDepth, true, nil)
}