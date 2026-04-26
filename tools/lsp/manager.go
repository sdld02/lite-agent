package lsp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager 管理多个 LSP 服务器实例，根据文件扩展名路由请求。
//
// 核心职责：
//  1. 维护 extensionMap（扩展名 → 服务器名称）路由表
//  2. 惰性启动（按需启动 LSP 子进程）
//  3. 文件同步（didOpen/didChange/didClose）
//  4. gitignore 结果过滤
type Manager struct {
	mu      sync.Mutex
	servers map[string]*ServerInstance   // serverName → instance
	extMap  extensionMap                  // extension → config
	configs []LspServerConfig             // 所有配置

	// 已打开文件跟踪
	openedFiles map[string]string // fileURI → serverName

	// 工作区根目录
	workDir string
}

// NewManager 创建 LSP 服务器管理器
func NewManager(workDir string) *Manager {
	return &Manager{
		servers:     make(map[string]*ServerInstance),
		extMap:      make(extensionMap),
		openedFiles: make(map[string]string),
		workDir:     workDir,
	}
}

// Initialize 加载服务器配置（不启动任何进程）
func (m *Manager) Initialize(configs []LspServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.configs = configs
	m.extMap = buildExtensionMap(configs)

	// 为每个配置创建 ServerInstance（惰性）
	for _, cfg := range configs {
		cfgCopy := cfg // 避免闭包引用
		cfgCopy.WorkspaceFolder = m.workDir
		m.servers[cfg.Name] = NewServerInstance(cfg.Name, &cfgCopy)
	}

	return nil
}

// Shutdown 关闭所有 LSP 服务器
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, srv := range m.servers {
		if err := srv.Stop(); err != nil {
			errs = append(errs, err)
		}
	}

	m.servers = make(map[string]*ServerInstance)
	m.openedFiles = make(map[string]string)

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// RegisterServer 动态注册额外的服务器配置
func (m *Manager) RegisterServer(config LspServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.configs = append(m.configs, config)
	for _, ext := range config.Extensions {
		normalized := strings.ToLower(ext)
		if _, exists := m.extMap[normalized]; !exists {
			m.extMap[normalized] = &m.configs[len(m.configs)-1]
		}
	}
	cfgCopy := config
	cfgCopy.WorkspaceFolder = m.workDir
	m.servers[config.Name] = NewServerInstance(config.Name, &cfgCopy)
}

// GetServerForFile 根据文件路径获取对应的 LSP 服务器
func (m *Manager) GetServerForFile(filePath string) *ServerInstance {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.getServerForFileLocked(filePath)
}

func (m *Manager) getServerForFileLocked(filePath string) *ServerInstance {
	ext := strings.ToLower(filepath.Ext(filePath))
	cfg, ok := m.extMap[ext]
	if !ok {
		return nil
	}
	return m.servers[cfg.Name]
}

// SendRequest 向对应文件的 LSP 服务器发送请求。
//
// 如果服务器尚未启动，自动启动。
// 如果文件尚未打开，自动发送 didOpen。
func (m *Manager) SendRequest(filePath string, method string, params interface{}) (json.RawMessage, error) {
	m.mu.Lock()
	server := m.getServerForFileLocked(filePath)
	m.mu.Unlock()

	if server == nil {
		return nil, fmt.Errorf("no LSP server available for file type: %s", filepath.Ext(filePath))
	}

	// 确保服务器已启动
	if err := m.ensureStarted(server); err != nil {
		return nil, err
	}

	// 发送请求
	result, err := server.SendRequest(method, params)
	if err != nil {
		// 标记为 error，下次重试
		server.State = StateError
		server.lastError = err
		server.crashCount++
	}
	return result, err
}

// OpenFile 发送 textDocument/didOpen 通知
func (m *Manager) OpenFile(filePath string, content string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}
	fileURI := pathToURI(absPath)

	m.mu.Lock()
	server := m.getServerForFileLocked(absPath)
	m.mu.Unlock()

	if server == nil {
		return fmt.Errorf("no LSP server available for file type: %s", filepath.Ext(absPath))
	}

	if err := m.ensureStarted(server); err != nil {
		return err
	}

	// 检查是否已打开
	m.mu.Lock()
	if existing, ok := m.openedFiles[fileURI]; ok && existing == server.Name {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	ext := strings.ToLower(filepath.Ext(absPath))
	langID := server.Config.lookupLanguageID(ext)
	if langID == "" {
		langID = "plaintext"
	}

	err = server.SendNotification("textDocument/didOpen", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        fileURI,
			"languageId": langID,
			"version":    1,
			"text":       content,
		},
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.openedFiles[fileURI] = server.Name
	m.mu.Unlock()

	return nil
}

// IsFileOpen 检查文件是否已在对应 LSP 服务器上打开
func (m *Manager) IsFileOpen(filePath string) bool {
	absPath, _ := filepath.Abs(filePath)
	fileURI := pathToURI(absPath)

	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.openedFiles[fileURI]
	return ok
}

// IsHealthy 检查是否至少有一个服务器处于健康状态
func (m *Manager) IsHealthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, srv := range m.servers {
		if srv.IsHealthy() {
			return true
		}
	}
	return false
}

// HasConfiguredServers 检查是否至少配置了一个 LSP 服务器（不管是否已启动）
func (m *Manager) HasConfiguredServers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.servers) > 0
}

// GetWorkDir 返回工作区目录
func (m *Manager) GetWorkDir() string {
	return m.workDir
}

// FilterGitIgnored 使用 git check-ignore 过滤被 gitignore 排除的文件位置
func FilterGitIgnored(locations []Location, workDir string) []Location {
	if len(locations) == 0 {
		return locations
	}

	// 收集唯一路径
	pathSet := make(map[string]bool)
	uriToPath := make(map[string]string)
	for _, loc := range locations {
		if loc.URI != "" && !pathSet[loc.URI] {
			pathSet[loc.URI] = true
			uriToPath[loc.URI] = uriToFilePath(loc.URI)
		}
	}

	var uniquePaths []string
	seen := make(map[string]bool)
	for _, p := range uriToPath {
		if !seen[p] {
			seen[p] = true
			uniquePaths = append(uniquePaths, p)
		}
	}

	if len(uniquePaths) == 0 {
		return locations
	}

	// 分批调用 git check-ignore
	ignoredPaths := make(map[string]bool)
	batchSize := 50

	for i := 0; i < len(uniquePaths); i += batchSize {
		end := i + batchSize
		if end > len(uniquePaths) {
			end = len(uniquePaths)
		}
		batch := uniquePaths[i:end]

		args := append([]string{"check-ignore"}, batch...)
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		output, err := cmd.Output()

		// git check-ignore: exit 0 = 至少一个被忽略, 1 = 无忽略, 128 = 非 git 仓库
		if err == nil && len(output) > 0 {
			for _, line := range strings.Split(string(output), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" {
					ignoredPaths[trimmed] = true
				}
			}
		}
	}

	if len(ignoredPaths) == 0 {
		return locations
	}

	// 过滤
	var filtered []Location
	for _, loc := range locations {
		p := uriToPath[loc.URI]
		if !ignoredPaths[p] {
			filtered = append(filtered, loc)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

func (m *Manager) ensureStarted(server *ServerInstance) error {
	if server.State == StateRunning && server.IsHealthy() {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return server.Start(m.workDir)
}

// ---------------------------------------------------------------------------
// 公共工具函数
// ---------------------------------------------------------------------------

// pathToURI 将文件路径转为 file:// URI
func pathToURI(filePath string) string {
	absPath, _ := filepath.Abs(filePath)
	// 在 macOS/Linux 上
	p := filepath.ToSlash(absPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

// uriToFilePath 将 file:// URI 转为文件路径
func uriToFilePath(uri string) string {
	// 去掉 "file://" 前缀
	p := strings.TrimPrefix(uri, "file://")
	// 解码 URL 编码
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return p
}

// ReadFileForLSP 读取文件内容供 LSP 使用（限制 10MB）
const MaxLSPFileSize = 10_000_000

func ReadFileForLSP(filePath string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	if info.Size() > MaxLSPFileSize {
		return "", fmt.Errorf("file too large for LSP analysis (%.0fMB exceeds 10MB limit)",
			float64(info.Size())/1_000_000)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// GetMethodAndParams 将 LSPTool operation 映射为 LSP 方法名和参数
func GetMethodAndParams(input LSPToolInput, filePath string) (method string, params interface{}) {
	uri := pathToURI(filePath)
	// 坐标系转换：1-based（用户输入）→ 0-based（LSP 协议）
	position := map[string]interface{}{
		"line":      input.Line - 1,
		"character": input.Character - 1,
	}

	switch input.Operation {
	case OpGoToDefinition:
		return "textDocument/definition", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	case OpFindReferences:
		return "textDocument/references", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
			"context":      map[string]bool{"includeDeclaration": true},
		}
	case OpHover:
		return "textDocument/hover", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	case OpDocumentSymbol:
		return "textDocument/documentSymbol", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
		}
	case OpWorkspaceSymbol:
		return "workspace/symbol", map[string]interface{}{
			"query": "",
		}
	case OpGoToImplementation:
		return "textDocument/implementation", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	case OpPrepareCallHierarchy:
		return "textDocument/prepareCallHierarchy", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	case OpIncomingCalls:
		// 第一步：prepareCallHierarchy
		return "textDocument/prepareCallHierarchy", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	case OpOutgoingCalls:
		return "textDocument/prepareCallHierarchy", map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     position,
		}
	default:
		return "", nil
	}
}
