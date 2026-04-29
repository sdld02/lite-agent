package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentFromFile 从 .md 文件加载自定义 Agent 定义。
//
// 文件格式（参考 Claude Code 的 agent markdown 格式）：
//
//	---
//	name: my-agent
//	description: Short description of when to use this agent
//	tools: Read, Grep, Glob
//	model: inherit
//	---
//	You are a specialized agent...
//
// 支持的 frontmatter 字段：
//   - name (必填): Agent 类型标识
//   - description (必填): 何时使用此 Agent
//   - tools: 允许的工具列表，逗号分隔，"*" 表示所有
//   - disallowedTools: 禁止的工具列表，逗号分隔
//   - model: 模型名称，"inherit" 表示继承父Agent
//   - maxTurns: 最大对话轮数
//   - background: 是否后台运行（true/false）
//   - color: Agent 颜色标记
func LoadAgentFromFile(filePath string, source AgentSource) (*AgentDefinition, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read agent file: %w", err)
	}

	content := string(data)

	// 解析 frontmatter
	frontmatter, body, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	// 验证必填字段
	agentType := frontmatter["name"]
	if agentType == "" {
		return nil, fmt.Errorf("missing 'name' in frontmatter")
	}

	whenToUse := frontmatter["description"]
	if whenToUse == "" {
		return nil, fmt.Errorf("missing 'description' in frontmatter")
	}

	// 解析工具列表
	var tools []string
	if toolsRaw := frontmatter["tools"]; toolsRaw != "" {
		tools = parseCommaList(toolsRaw)
	}

	var disallowedTools []string
	if disallowedRaw := frontmatter["disallowedTools"]; disallowedRaw != "" {
		disallowedTools = parseCommaList(disallowedRaw)
	}

	// 构建定义
	def := &AgentDefinition{
		AgentType:       agentType,
		WhenToUse:       whenToUse,
		Source:          source,
		Tools:           tools,
		DisallowedTools: disallowedTools,
		SystemPrompt:    strings.TrimSpace(body),
		Filename:        strings.TrimSuffix(filepath.Base(filePath), ".md"),
		BaseDir:         filepath.Dir(filePath),
	}

	// 可选字段
	if model := frontmatter["model"]; model != "" {
		def.Model = model
	}
	if color := frontmatter["color"]; color != "" {
		def.Color = color
	}

	return def, nil
}

// LoadAgentsFromDir 从目录加载所有 .md 文件作为 Agent 定义
func LoadAgentsFromDir(dirPath string, source AgentSource) ([]AgentDefinition, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在，不是错误
		}
		return nil, fmt.Errorf("read agents dir: %w", err)
	}

	var defs []AgentDefinition
	var errors []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(dirPath, entry.Name())
		def, err := LoadAgentFromFile(filePath, source)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}

		defs = append(defs, *def)
	}

	if len(errors) > 0 {
		return defs, fmt.Errorf("errors loading agents: %s", strings.Join(errors, "; "))
	}

	return defs, nil
}

// parseFrontmatter 解析 YAML-like frontmatter
// 支持简单的 key: value 格式（不支持嵌套结构）
func parseFrontmatter(content string) (map[string]string, string, error) {
	content = strings.TrimSpace(content)

	if !strings.HasPrefix(content, "---") {
		// 没有 frontmatter，整个内容作为 body
		return map[string]string{}, content, nil
	}

	// 找到第二个 ---
	rest := strings.TrimPrefix(content, "---")
	endIdx := strings.Index(rest, "---")
	if endIdx == -1 {
		return map[string]string{}, strings.TrimSpace(rest), nil
	}

	frontmatterRaw := strings.TrimSpace(rest[:endIdx])
	body := strings.TrimSpace(rest[endIdx+3:])

	frontmatter := make(map[string]string)

	for _, line := range strings.Split(frontmatterRaw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// 移除引号
		value = strings.Trim(value, "\"'")

		frontmatter[key] = value
	}

	return frontmatter, body, nil
}

// parseCommaList 解析逗号分隔的列表
func parseCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
