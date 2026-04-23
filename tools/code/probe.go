package code

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type OutputMode string

const (
	ModeSummary   OutputMode = "summary"   // 仅统计信息
	ModeStructure OutputMode = "structure" // 目录树结构（可限制数量）
	ModeFlat      OutputMode = "flat"      // 扁平列表（适合 grep）
	ModeGrouped   OutputMode = "grouped"   // 按扩展名分组
)

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
func GetProjectSummary(rootPath string, maxDepth int) ([]byte, error) {
	stats := &ProjectStats{
		FileTypes: make(map[string]int),
	}
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == rootPath {
			return err
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

func GetSmartTree(rootPath string, maxDepth int, maxItemsPerDir int) ([]byte, error) {
	absPath, _ := filepath.Abs(rootPath)
	root := &FileNode{
		Name:  filepath.Base(absPath),
		Path:  absPath,
		IsDir: true,
	}
	
	totalDirs, totalFiles := 0, 0
	buildSmartTree(absPath, root, 0, maxDepth, maxItemsPerDir, &totalDirs, &totalFiles)
	
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

func buildSmartTree(path string, node *FileNode, depth, maxDepth, maxItems int, totalDirs, totalFiles *int) {
	if depth >= maxDepth {
		return
	}
	
	entries, _ := os.ReadDir(path)
	
	// 限制每层显示数量
	if len(entries) > maxItems {
		entries = entries[:maxItems]
		node.Truncated = true
	}
	
	for _, entry := range entries {
		if entry.IsDir() {
			*totalDirs++
			child := &FileNode{
				Name:  entry.Name(),
				Path:  filepath.Join(node.Path, entry.Name()),
				IsDir: true,
			}
			buildSmartTree(filepath.Join(path, entry.Name()), child, depth+1, maxDepth, maxItems, totalDirs, totalFiles)
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
func GetFlatList(rootPath string, maxDepth int, maxItems int) ([]byte, error) {
	var files []string
	var dirs []string
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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

func GetGroupedByType(rootPath string, maxDepth int) ([]byte, error) {
	grouped := &GroupedStructure{
		ByType:  make(map[string][]string),
		Summary: make(map[string]int),
	}
	
	err := filepath.WalkDir(rootPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
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