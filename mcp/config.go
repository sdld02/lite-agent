package mcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

const (
	// MCPConfigFileName MCP 配置文件名
	MCPConfigFileName = "mcp.json"
)

// LoadConfig 加载并合并 MCP 配置。
//
// 加载顺序（后面的覆盖前面的同名服务器）：
//  1. ~/.lite-agent/mcp.json（用户级）
//  2. <projectRoot>/.lite-agent/mcp.json（项目级）
func LoadConfig(homeDir, projectRoot string) []ServerConfig {
	var allConfigs []ServerConfig

	// 1. 加载用户级配置
	userConfigPath := filepath.Join(homeDir, ".lite-agent", MCPConfigFileName)
	userConfigs := loadConfigFile(userConfigPath)
	allConfigs = append(allConfigs, userConfigs...)

	// 2. 加载项目级配置
	if projectRoot != "" {
		projectConfigPath := filepath.Join(projectRoot, ".lite-agent", MCPConfigFileName)
		projectConfigs := loadConfigFile(projectConfigPath)
		allConfigs = append(allConfigs, projectConfigs...)
	}

	// 3. 合并：后面的覆盖前面的同名服务器
	return mergeConfigs(allConfigs)
}

// loadConfigFile 加载单个 mcp.json 文件
func loadConfigFile(path string) []ServerConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[MCP] 读取配置文件失败 %s: %v", path, err)
		}
		return nil
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		log.Printf("[MCP] 解析配置文件失败 %s: %v", path, err)
		return nil
	}

	log.Printf("[MCP] 已加载配置: %s (%d 个服务器)", path, len(config.Servers))

	// 验证配置
	valid := make([]ServerConfig, 0, len(config.Servers))
	for _, s := range config.Servers {
		if s.Name == "" {
			log.Printf("[MCP] 跳过无效配置（缺少 name）: %s", path)
			continue
		}
		if s.Command == "" {
			log.Printf("[MCP] 跳过服务器 %s（缺少 command）", s.Name)
			continue
		}
		valid = append(valid, s)
	}

	return valid
}

// mergeConfigs 合并服务器配置，后面的覆盖前面的同名服务器
func mergeConfigs(configs []ServerConfig) []ServerConfig {
	seen := make(map[string]int) // name → index in result

	merged := make([]ServerConfig, 0)
	for _, cfg := range configs {
		if idx, exists := seen[cfg.Name]; exists {
			// 覆盖
			log.Printf("[MCP] 项目级配置覆盖用户级服务器: %s", cfg.Name)
			merged[idx] = cfg
		} else {
			seen[cfg.Name] = len(merged)
			merged = append(merged, cfg)
		}
	}

	return merged
}

// HasConfigFile 检查是否存在 mcp.json 配置文件
func HasConfigFile(homeDir, projectRoot string) bool {
	if _, err := os.Stat(filepath.Join(homeDir, ".lite-agent", MCPConfigFileName)); err == nil {
		return true
	}
	if projectRoot != "" {
		if _, err := os.Stat(filepath.Join(projectRoot, ".lite-agent", MCPConfigFileName)); err == nil {
			return true
		}
	}
	return false
}

// SaveUserConfig 将用户级 MCP 配置原子写入 ~/.lite-agent/mcp.json
func SaveUserConfig(homeDir string, servers []ServerConfig) error {
	configDir := filepath.Join(homeDir, ".lite-agent")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	cfg := MCPConfig{Servers: servers}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}

	// 原子写入：先写临时文件，再重命名
	configPath := filepath.Join(configDir, MCPConfigFileName)
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write tmp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}

	log.Printf("[MCP] 已保存用户级配置: %s (%d 个服务器)", configPath, len(servers))
	return nil
}

// CreateExampleConfig 创建示例配置文件
func CreateExampleConfig(dir string) error {
	configDir := filepath.Join(dir, ".lite-agent")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	configPath := filepath.Join(configDir, MCPConfigFileName)

	// 如果已存在，不覆盖
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	example := MCPConfig{
		Servers: []ServerConfig{
			{
				Name:    "filesystem",
				Command: "npx",
				Args:    []string{"-y", "@anthropic-ai/mcp-server-filesystem", "/path/to/allowed/dir"},
			},
		},
	}

	data, err := json.MarshalIndent(example, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal example config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("write example config: %w", err)
	}

	log.Printf("[MCP] 已创建示例配置文件: %s", configPath)
	return nil
}
