package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	globalManager *Manager
	managerMu     sync.Mutex
)

// InitGlobalManager 初始化全局 LSP 管理器（应在 agent 启动时调用一次）
func InitGlobalManager(workDir string) *Manager {
	managerMu.Lock()
	defer managerMu.Unlock()

	mgr := NewManager(workDir)
	if err := mgr.Initialize(DefaultServerConfigs()); err != nil {
		fmt.Fprintf(os.Stderr, "[lsp] failed to initialize manager: %v\n", err)
	}
	globalManager = mgr
	return mgr
}

// GetGlobalManager 获取全局 LSP 管理器
func GetGlobalManager() *Manager {
	managerMu.Lock()
	defer managerMu.Unlock()
	return globalManager
}

// SetGlobalManager 设置全局 LSP 管理器（用于测试或自定义配置）
func SetGlobalManager(mgr *Manager) {
	managerMu.Lock()
	defer managerMu.Unlock()
	globalManager = mgr
}

// IsAvailable 检查 LSP 是否可用（至少有一个服务器配置了）
func IsAvailable() bool {
	mgr := GetGlobalManager()
	if mgr == nil {
		return false
	}
	return mgr.HasConfiguredServers()
}

// ExecuteLSPOperation 执行 LSP 操作的顶层入口。
//
// 这是 LSP Tool 的核心逻辑，完整实现了 Claude Code LSPTool 的所有功能：
//   - 输入验证（文件存在性、operation 合法性）
//   - 坐标系转换（1-based → 0-based）
//   - 文件自动打开（didOpen）
//   - LSP 请求路由（扩展名 → 服务器）
//   - Call Hierarchy 两步编排
//   - gitignore 结果过滤
//   - 结果格式化 + 统计计数
//   - 防御性错误处理
func ExecuteLSPOperation(input LSPToolInput) (*LSPToolOutput, error) {
	// 1. 验证 operation
	if !IsValidOperation(string(input.Operation)) {
		return nil, fmt.Errorf("invalid operation: %s", input.Operation)
	}

	// 2. 解析绝对路径
	absPath, err := filepath.Abs(input.FilePath)
	if err != nil {
		return nil, fmt.Errorf("resolve file path: %w", err)
	}

	// 3. 验证文件存在
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file does not exist: %s", input.FilePath)
		}
		return nil, fmt.Errorf("cannot access file: %s: %w", input.FilePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file: %s", input.FilePath)
	}

	// 4. 获取管理器
	mgr := GetGlobalManager()
	if mgr == nil {
		return &LSPToolOutput{
			Operation: input.Operation,
			Result:    "LSP server manager not initialized.",
			FilePath:  input.FilePath,
		}, nil
	}

	// 5. 确保文件已在 LSP 服务器上打开
	if !mgr.IsFileOpen(absPath) {
		content, err := ReadFileForLSP(absPath)
		if err != nil {
			return &LSPToolOutput{
				Operation: input.Operation,
				Result:    fmt.Sprintf("Cannot read file: %v", err),
				FilePath:  input.FilePath,
			}, nil
		}
		if err := mgr.OpenFile(absPath, content); err != nil {
			return &LSPToolOutput{
				Operation: input.Operation,
				Result:    fmt.Sprintf("Cannot open file in LSP server: %v", err),
				FilePath:  input.FilePath,
			}, nil
		}
	}

	// 6. 获取 LSP 方法和参数
	method, params := GetMethodAndParams(input, absPath)
	if method == "" {
		return nil, fmt.Errorf("unknown operation: %s", input.Operation)
	}

	// 7. 发送请求
	result, err := mgr.SendRequest(absPath, method, params)
	if err != nil {
		return &LSPToolOutput{
			Operation: input.Operation,
			Result:    fmt.Sprintf("Error performing %s: %v", input.Operation, err),
			FilePath:  input.FilePath,
		}, nil
	}

	if result == nil || string(result) == "null" {
		ext := filepath.Ext(absPath)
		return &LSPToolOutput{
			Operation: input.Operation,
			Result:    fmt.Sprintf("No LSP server available for file type: %s", ext),
			FilePath:  input.FilePath,
		}, nil
	}

	// 8. 对于 incomingCalls / outgoingCalls，需要两步编排
	if input.Operation == OpIncomingCalls || input.Operation == OpOutgoingCalls {
		return executeCallHierarchy(input, absPath, result, mgr)
	}

	// 9. 过滤 gitignored 的结果
	result = filterResultByGitIgnore(input, result, mgr.GetWorkDir())

	// 10. 格式化结果
	formatted, resultCount, fileCount := formatOperationResult(input.Operation, result, mgr.GetWorkDir())

	return &LSPToolOutput{
		Operation:   input.Operation,
		Result:      formatted,
		FilePath:    input.FilePath,
		ResultCount: resultCount,
		FileCount:   fileCount,
	}, nil
}

// executeCallHierarchy 处理 Call Hierarchy 的两步协议编排：
//
//	Step 1: textDocument/prepareCallHierarchy → CallHierarchyItem[]
//	Step 2: callHierarchy/incomingCalls 或 callHierarchy/outgoingCalls
func executeCallHierarchy(
	input LSPToolInput,
	absPath string,
	prepareResult json.RawMessage,
	mgr *Manager,
) (*LSPToolOutput, error) {
	// 解析 prepareCallHierarchy 结果
	var items []CallHierarchyItem
	if err := json.Unmarshal(prepareResult, &items); err != nil || len(items) == 0 {
		return &LSPToolOutput{
			Operation:   input.Operation,
			Result:      "No call hierarchy item found at this position",
			FilePath:    input.FilePath,
			ResultCount: 0,
			FileCount:   0,
		}, nil
	}

	// 取第一个 CallHierarchyItem
	item := items[0]

	var callMethod string
	if input.Operation == OpIncomingCalls {
		callMethod = "callHierarchy/incomingCalls"
	} else {
		callMethod = "callHierarchy/outgoingCalls"
	}

	callResult, err := mgr.SendRequest(absPath, callMethod, map[string]interface{}{
		"item": item,
	})
	if err != nil {
		return &LSPToolOutput{
			Operation: input.Operation,
			Result:    fmt.Sprintf("Error performing %s (step 2): %v", input.Operation, err),
			FilePath:  input.FilePath,
		}, nil
	}

	// 格式化
	formatted, resultCount, fileCount := formatOperationResult(input.Operation, callResult, mgr.GetWorkDir())

	return &LSPToolOutput{
		Operation:   input.Operation,
		Result:      formatted,
		FilePath:    input.FilePath,
		ResultCount: resultCount,
		FileCount:   fileCount,
	}, nil
}

// formatOperationResult 根据操作类型分发格式化
func formatOperationResult(op Operation, raw json.RawMessage, workDir string) (string, int, int) {
	switch op {
	case OpGoToDefinition, OpGoToImplementation:
		return FormatGoToDefinition(raw, workDir)
	case OpFindReferences:
		return FormatFindReferences(raw, workDir)
	case OpHover:
		return FormatHover(raw, workDir)
	case OpDocumentSymbol:
		return FormatDocumentSymbol(raw, workDir)
	case OpWorkspaceSymbol:
		return FormatWorkspaceSymbol(raw, workDir)
	case OpPrepareCallHierarchy:
		return FormatPrepareCallHierarchy(raw, workDir)
	case OpIncomingCalls:
		return FormatIncomingCalls(raw, workDir)
	case OpOutgoingCalls:
		return FormatOutgoingCalls(raw, workDir)
	default:
		return fmt.Sprintf("Unknown operation: %s", op), 0, 0
	}
}

// filterResultByGitIgnore 对位置相关的结果进行 gitignore 过滤
func filterResultByGitIgnore(input LSPToolInput, raw json.RawMessage, workDir string) json.RawMessage {
	// 只有这些 operation 返回位置列表，需要过滤
	switch input.Operation {
	case OpFindReferences, OpGoToDefinition, OpGoToImplementation, OpWorkspaceSymbol:
		// 需要过滤
	default:
		return raw
	}

	if input.Operation == OpWorkspaceSymbol {
		var symbols []SymbolInformation
		if err := json.Unmarshal(raw, &symbols); err != nil || len(symbols) == 0 {
			return raw
		}
		var locations []Location
		for _, sym := range symbols {
			if sym.Location.URI != "" {
				locations = append(locations, sym.Location)
			}
		}
		filtered := FilterGitIgnored(locations, workDir)
		filteredURIs := make(map[string]bool)
		for _, loc := range filtered {
			filteredURIs[loc.URI] = true
		}
		var kept []SymbolInformation
		for _, sym := range symbols {
			if sym.Location.URI == "" || filteredURIs[sym.Location.URI] {
				kept = append(kept, sym)
			}
		}
		newRaw, _ := json.Marshal(kept)
		return newRaw
	}

	// Location[] 类型的结果
	var locations []Location
	if err := json.Unmarshal(raw, &locations); err == nil {
		filtered := FilterGitIgnored(locations, workDir)
		newRaw, _ := json.Marshal(filtered)
		return newRaw
	}

	return raw
}

// ShutdownGlobalManager 关闭全局管理器（应在 agent 退出时调用）
func ShutdownGlobalManager() error {
	managerMu.Lock()
	defer managerMu.Unlock()

	if globalManager != nil {
		err := globalManager.Shutdown()
		globalManager = nil
		return err
	}
	return nil
}
