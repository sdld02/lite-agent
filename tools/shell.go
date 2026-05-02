package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"lite-agent/agent"
)

// ShellTool Shell 命令执行工具
type ShellTool struct {
	allowedCommands map[string]bool // 允许执行的命令白名单
}

// NewShellTool 创建 Shell 工具（带默认安全白名单）
func NewShellTool() *ShellTool {
	// 默认允许的安全命令
	allowed := map[string]bool{
		// Linux/Mac 常用命令
		"ls":     true,
		"pwd":    true,
		"echo":   true,
		"cat":    true,
		"whoami": true,
		"date":   true,
		"ping":   true,
		"curl":   true,
		"wget":   true,
		"git":    true,
		"go":     true,
		"npm":    true,
		"node":   true,
		"python": true,
		"docker": true,

		// Windows 常用命令
		"dir":        true,
		"cd":         true,
		"type":       true, // 类似 cat
		"copy":       true,
		"move":       true,
		"del":        true,
		"mkdir":      true,
		"rmdir":      true,
		"ren":        true, // 重命名
		"find":       true,
		"findstr":    true, // 类似 grep
		"tasklist":   true,
		"taskkill":   true,
		"netstat":    true,
		"ipconfig":   true,
		"systeminfo": true,
		"hostname":   true,
		"where":      true, // 类似 which
		"more":       true,
		"attrib":     true,
		"tree":       true,
		"wmic":       true,
		"chcp":       true,

		// PowerShell 常用命令
		"Get-Content":      true,
		"Get-ChildItem":    true,
		"Get-Location":     true,
		"Get-Process":      true,
		"Get-Service":      true,
		"Get-NetIPAddress": true,
		"Get-ComputerInfo": true,
		"Write-Output":     true,
		"Test-Connection":  true, // 类似 ping
		"Select-String":    true, // 类似 grep
	}

	return &ShellTool{
		allowedCommands: allowed,
	}
}

// NewShellToolUnsafe 创建不限制命令的 Shell 工具（慎用！）
func NewShellToolUnsafe() *ShellTool {
	return &ShellTool{
		allowedCommands: nil, // nil 表示不限制
	}
}

func (t *ShellTool) Name() string {
	return "shell"
}

func (t *ShellTool) Description() string {
	return "执行 shell 命令并返回输出结果。可以执行系统命令如 ls, dir, pwd, git 等。"
}

func (t *ShellTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "要执行的 shell 命令，如: ls -la, dir, git status",
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "命令的意图，如: 获取文件列表, 查看目录, 获取 Git 状态",
			},
		},
		"required": []string{"command", "intent"},
	}
}

func (t *ShellTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	command, ok := args["command"].(string)
	if !ok {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("command 参数必须是字符串"),
			IsError: true,
		}, nil
	}
	_, ok = args["intent"].(string)
	if !ok {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("intent 参数必须是字符串"),
			IsError: true,
		}, nil
	}

	// 安全检查：验证命令是否在白名单中
	if t.allowedCommands != nil {
		// 提取命令名（第一个单词）
		parts := strings.Fields(command)
		if len(parts) == 0 {
			return &agent.ToolResult{
				Content: agent.FormatValidationError("命令不能为空"),
				IsError: true,
			}, nil
		}

		cmdName := parts[0]
		if !t.allowedCommands[cmdName] {
			allowedList := make([]string, 0, len(t.allowedCommands))
			for k := range t.allowedCommands {
				allowedList = append(allowedList, k)
			}
			return &agent.ToolResult{
				Content: agent.FormatToolError(fmt.Errorf("命令 '%s' 不在允许列表中。允许的命令: %v", cmdName, allowedList)),
				IsError: true,
			}, nil
		}
	}

	// 执行命令
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		psCommand := fmt.Sprintf("chcp 65001 > $null; %s", command)
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", psCommand)
		cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	// 获取输出
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &agent.ToolResult{
			Content: fmt.Sprintf("命令执行失败: %v\n输出: %s", err, string(output)),
		}, nil
	}

	if len(output) == 0 {
		return &agent.ToolResult{Content: "命令执行成功（无输出）"}, nil
	}

	// 限制输出长度
	result := string(output)
	if len(result) > 10000 {
		result = result[:10000] + "\n... (输出被截断)"
	}

	return &agent.ToolResult{Content: result}, nil
}

// AddAllowedCommand 添加允许的命令到白名单
func (t *ShellTool) AddAllowedCommand(cmd string) {
	if t.allowedCommands != nil {
		t.allowedCommands[cmd] = true
	}
}

// RemoveAllowedCommand 从白名单移除命令
func (t *ShellTool) RemoveAllowedCommand(cmd string) {
	if t.allowedCommands != nil {
		delete(t.allowedCommands, cmd)
	}
}
