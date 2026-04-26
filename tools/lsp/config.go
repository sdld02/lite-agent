package lsp

import (
	"path/filepath"
	"strings"
)

// LspServerConfig 描述一个 LSP 服务器的启动配置
type LspServerConfig struct {
	// Name 唯一标识符（如 "gopls", "typescript"）
	Name string
	// Command 启动命令
	Command string
	// Args 命令行参数
	Args []string
	// Extensions 此服务器支持的文件扩展名（含点号，如 ".go", ".ts"）
	Extensions []string
	// ExtensionToLanguage 扩展名 → LSP languageId 映射
	ExtensionToLanguage map[string]string
	// WorkspaceFolder 工作区目录（默认为项目根）
	WorkspaceFolder string
	// InitializationOptions 服务器初始化选项
	InitializationOptions interface{}
	// StartupTimeoutMs 启动超时（毫秒）
	StartupTimeoutMs int
	// Env 额外的环境变量
	Env map[string]string
}

// DefaultServerConfigs 返回内置的 LSP 服务器配置。
//
// 每个配置定义了：启动命令、参数、支持的文件扩展名、以及扩展名到 LSP languageId 的映射。
// 用户可通过 RegisterServerConfig() 覆盖或添加新配置。
func DefaultServerConfigs() []LspServerConfig {
	return []LspServerConfig{
		{
			Name:    "gopls",
			Command: "gopls",
			Extensions: []string{".go"},
			ExtensionToLanguage: map[string]string{
				".go": "go",
			},
			StartupTimeoutMs: 30000,
		},
		{
			Name:    "typescript",
			Command: "typescript-language-server",
			Args:    []string{"--stdio"},
			Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
			ExtensionToLanguage: map[string]string{
				".ts":  "typescript",
				".tsx": "typescriptreact",
				".js":  "javascript",
				".jsx": "javascriptreact",
				".mjs": "javascript",
				".cjs": "javascript",
			},
			StartupTimeoutMs: 30000,
		},
		{
			Name:    "pyright",
			Command: "pyright-langserver",
			Args:    []string{"--stdio"},
			Extensions: []string{".py", ".pyi"},
			ExtensionToLanguage: map[string]string{
				".py":  "python",
				".pyi": "python",
			},
			StartupTimeoutMs: 30000,
		},
		{
			Name:    "rust-analyzer",
			Command: "rust-analyzer",
			Extensions: []string{".rs"},
			ExtensionToLanguage: map[string]string{
				".rs": "rust",
			},
			StartupTimeoutMs: 60000,
		},
	}
}

// lookupLanguageID 从配置中查找扩展名对应的 languageId
func (c *LspServerConfig) lookupLanguageID(ext string) string {
	normalized := strings.ToLower(ext)
	if c.ExtensionToLanguage != nil {
		if langID, ok := c.ExtensionToLanguage[normalized]; ok {
			return langID
		}
	}
	return ""
}

// matchesExtension 检查文件是否匹配此服务器的扩展名
func (c *LspServerConfig) matchesExtension(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	for _, e := range c.Extensions {
		if strings.ToLower(e) == ext {
			return true
		}
	}
	return false
}

// extensionMap 扩展名 → 服务器配置 的路由表
type extensionMap map[string]*LspServerConfig

// buildExtensionMap 根据服务器配置列表构建扩展名路由表
func buildExtensionMap(configs []LspServerConfig) extensionMap {
	m := make(extensionMap)
	for i := range configs {
		cfg := &configs[i]
		for _, ext := range cfg.Extensions {
			normalized := strings.ToLower(ext)
			// 第一个注册的优先
			if _, exists := m[normalized]; !exists {
				m[normalized] = cfg
			}
		}
	}
	return m
}
