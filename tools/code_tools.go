package tools

import (
	"context"
	"fmt"

	"lite-agent/tools/code"
)

// ==================== CodeProbe 工具包装器 ====================

// CodeProbeToolWrapper 代码探针工具，包装 code 包中的项目结构探查功能
type CodeProbeToolWrapper struct{}

func NewCodeProbeTool() *CodeProbeToolWrapper {
	return &CodeProbeToolWrapper{}
}

func (t *CodeProbeToolWrapper) Name() string {
	return "code_probe"
}

func (t *CodeProbeToolWrapper) Description() string {
	return "探查项目结构，支持多种模式：summary（摘要统计）、structure（目录树结构）、flat（扁平文件列表）、grouped（按扩展名分组）。返回 JSON 格式结果。"
}

func (t *CodeProbeToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "探查模式：summary（摘要）、structure（树形结构）、flat（扁平列表）、grouped（按类型分组）",
			},
			"root_path": map[string]interface{}{
				"type":        "string",
				"description": "项目根路径，默认为当前目录",
			},
			"max_depth": map[string]interface{}{
				"type":        "integer",
				"description": "最大递归深度，默认为 3",
			},
			"max_items": map[string]interface{}{
				"type":        "integer",
				"description": "最大显示项数（仅 flat 模式有效），默认为 200",
			},
			"max_items_per_dir": map[string]interface{}{
				"type":        "integer",
				"description": "每层最大显示项数（仅 structure 模式有效），默认为 15",
			},
		},
		"required": []string{"mode"},
	}
}

func (t *CodeProbeToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	mode, _ := args["mode"].(string)
	rootPath, _ := args["root_path"].(string)
	if rootPath == "" {
		rootPath = "."
	}

	maxDepth := 3
	if v, ok := args["max_depth"].(float64); ok {
		maxDepth = int(v)
	}

	switch mode {
	case "summary":
		result, err := code.GetProjectSummary(rootPath, maxDepth)
		if err != nil {
			return "", fmt.Errorf("获取项目摘要失败: %v", err)
		}
		return string(result), nil

	case "structure":
		maxItemsPerDir := 15
		if v, ok := args["max_items_per_dir"].(float64); ok {
			maxItemsPerDir = int(v)
		}
		result, err := code.GetSmartTree(rootPath, maxDepth, maxItemsPerDir)
		if err != nil {
			return "", fmt.Errorf("获取目录树失败: %v", err)
		}
		return string(result), nil

	case "flat":
		maxItems := 200
		if v, ok := args["max_items"].(float64); ok {
			maxItems = int(v)
		}
		result, err := code.GetFlatList(rootPath, maxDepth, maxItems)
		if err != nil {
			return "", fmt.Errorf("获取扁平列表失败: %v", err)
		}
		return string(result), nil

	case "grouped":
		result, err := code.GetGroupedByType(rootPath, maxDepth)
		if err != nil {
			return "", fmt.Errorf("获取类型分组失败: %v", err)
		}
		return string(result), nil

	default:
		return "", fmt.Errorf("不支持的探查模式: %s，支持的模式: summary, structure, flat, grouped", mode)
	}
}

// ==================== CodeStats 工具包装器 ====================

// CodeStatsToolWrapper 代码统计工具，包装 code 包中的代码行数统计功能
type CodeStatsToolWrapper struct{}

func NewCodeStatsTool() *CodeStatsToolWrapper {
	return &CodeStatsToolWrapper{}
}

func (t *CodeStatsToolWrapper) Name() string {
	return "code_stats"
}

func (t *CodeStatsToolWrapper) Description() string {
	return "统计指定路径下各语言的代码行数、注释行数、空行数。返回 JSON 格式统计结果，包含按语言分组的详细数据。"
}

func (t *CodeStatsToolWrapper) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"root_path": map[string]interface{}{
				"type":        "string",
				"description": "要统计的路径，默认为当前目录",
			},
			"workers": map[string]interface{}{
				"type":        "integer",
				"description": "并发工作协程数，默认为 8",
			},
			"exclude": map[string]interface{}{
				"type":        "string",
				"description": "排除的目录，逗号分隔，默认为 node_modules,.git,dist,build,vendor,__pycache__",
			},
		},
		"required": []string{},
	}
}

func (t *CodeStatsToolWrapper) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	rootPath, _ := args["root_path"].(string)
	if rootPath == "" {
		rootPath = "."
	}

	workers := 8
	if v, ok := args["workers"].(float64); ok {
		workers = int(v)
	}

	exclude := "node_modules,.git,dist,build,vendor,__pycache__"
	if v, ok := args["exclude"].(string); ok && v != "" {
		exclude = v
	}

	result, err := code.GetCodeStats(rootPath, workers, exclude)
	if err != nil {
		return "", fmt.Errorf("代码统计失败: %v", err)
	}

	return string(result), nil
}
