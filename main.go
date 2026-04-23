package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"lite-agent/agent"
	"lite-agent/llm"
	"lite-agent/session"
	"lite-agent/tools"

	"github.com/charmbracelet/glamour"
)

// 支持的 LLM 提供者预设配置
var llmProviders = map[string]struct {
	baseURL string
	model   string
}{
	"openai":   {"https://api.openai.com/v1", "gpt-4o"},
	"deepseek": {"https://api.deepseek.com/v1", "deepseek-chat"},
	"moonshot": {"https://api.moonshot.cn/v1", "moonshot-v1-8k"},
	"zhipu":    {"https://open.bigmodel.cn/api/paas/v4", "glm-4"},
	"qwen":     {"https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen-turbo"},
	"ollama":   {"http://localhost:11434/v1", "llama2"},
}

// getSystemInfo 动态获取系统信息
func getSystemInfo() map[string]interface{} {
	info := map[string]interface{}{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"cpus":    runtime.NumCPU(),
		"version": runtime.Version(),
	}

	// 获取当前用户
	if currentUser, err := user.Current(); err == nil {
		info["user"] = currentUser.Username
		info["homeDir"] = currentUser.HomeDir
	}

	// 获取工作目录
	if workDir, err := os.Getwd(); err == nil {
		info["workDir"] = workDir
	}

	// 获取主机名
	if hostname, err := os.Hostname(); err == nil {
		info["hostname"] = hostname
	}

	return info
}

// buildDefaultSystemPrompt 构建默认系统提示词（包含动态系统信息）
func buildDefaultSystemPrompt() string {
	sysInfo := getSystemInfo()

	return fmt.Sprintf(`你是一个智能助手，运行在以下系统环境中：

## 系统信息
- 操作系统: %s
- CPU 架构: %s
- CPU 核心数: %d
- Go 版本: %s
- 主机名: %s
- 当前用户: %s
- 用户主目录: %s
- 当前工作目录: %s

## 可用工具
你有以下工具可以使用：
- calculator: 执行数学计算
- system_info: 获取系统信息
- shell: 执行系统命令
- file_edit: 编辑文件内容（精确字符串替换）
- file_write: 写入文件内容（创建或覆盖文件）
- file_diff: 比较两个文件的差异
- file_read: 读取文件内容
- code_probe: 探查项目结构（支持 summary/structure/flat/grouped/tree 模式）
- code_stats: 统计代码行数（支持按语言分组统计）

## 行为准则
1. 当用户请求需要使用工具时，请调用相应的工具来完成任务
2. 如果用户的问题不需要使用工具，请直接回答
3. 执行 shell 命令时，注意当前操作系统是 %s，使用适合该系统的命令
4. 请用中文回复用户`,
		sysInfo["os"],
		sysInfo["arch"],
		sysInfo["cpus"],
		sysInfo["version"],
		sysInfo["hostname"],
		sysInfo["user"],
		sysInfo["homeDir"],
		sysInfo["workDir"],
		sysInfo["os"],
	)
}

func main() {
	// 命令行参数
	provider := flag.String("provider", "", "LLM 提供者: openai, deepseek, moonshot, zhipu, qwen, ollama")
	apiKey := flag.String("key", "", "API Key (也可通过环境变量设置)")
	baseURL := flag.String("url", "", "API Base URL (可选，默认使用 provider 预设)")
	model := flag.String("model", "", "模型名称 (可选，默认使用 provider 预设)")
	systemPrompt := flag.String("prompt", "", "系统提示词 (可选，默认使用内置提示词)")
	stream := flag.Bool("stream", true, "启用流式输出模式（默认开启，-stream=false 关闭）")
	newSession := flag.Bool("new", false, "强制开始新会话")
	sessionID := flag.String("session", "", "指定加载某个 session ID")
	flag.Parse()

	// 确定 API Key
	finalAPIKey := *apiKey
	if finalAPIKey == "" {
		finalAPIKey = os.Getenv("OPENAI_API_KEY")
	}

	// 确定 Base URL 和 Model
	var finalBaseURL, finalModel string
	if *provider != "" {
		// 使用预设提供者
		if p, ok := llmProviders[*provider]; ok {
			finalBaseURL = p.baseURL
			finalModel = p.model
		} else {
			fmt.Printf("未知的提供者: %s\n支持的提供者: ", *provider)
			for name := range llmProviders {
				fmt.Printf("%s ", name)
			}
			fmt.Println()
			os.Exit(1)
		}
	}

	// 命令行参数覆盖预设
	if *baseURL != "" {
		finalBaseURL = *baseURL
	}
	if *model != "" {
		finalModel = *model
	}

	// 环境变量覆盖
	if envURL := os.Getenv("OPENAI_BASE_URL"); envURL != "" {
		finalBaseURL = envURL
	}
	if envModel := os.Getenv("OPENAI_MODEL"); envModel != "" {
		finalModel = envModel
	}

	// 默认值
	if finalBaseURL == "" {
		finalBaseURL = "https://api.openai.com/v1"
	}
	if finalModel == "" {
		finalModel = "gpt-4o"
	}

	// 验证 API Key
	if finalAPIKey == "" {
		fmt.Println("=================================")
		fmt.Println("     Go AI Agent 学习框架")
		fmt.Println("=================================")
		fmt.Println()
		fmt.Println("❌ 请设置 API Key")
		fmt.Println()
		fmt.Println("方式一：环境变量")
		fmt.Println("  Windows PowerShell:")
		fmt.Println("    $env:OPENAI_API_KEY='your-api-key'")
		fmt.Println()
		fmt.Println("  Linux/Mac:")
		fmt.Println("    export OPENAI_API_KEY='your-api-key'")
		fmt.Println()
		fmt.Println("方式二：命令行参数")
		fmt.Println("  go run main.go -provider=deepseek -key=your-api-key")
		fmt.Println()
		fmt.Println("支持的提供者:")
		fmt.Println("  - openai    : GPT-4, GPT-4o")
		fmt.Println("  - deepseek  : DeepSeek Chat/Coder")
		fmt.Println("  - moonshot  : Kimi (月之暗面)")
		fmt.Println("  - zhipu     : GLM-4 (智谱)")
		fmt.Println("  - qwen      : 通义千问")
		fmt.Println("  - ollama    : 本地模型")
		fmt.Println("=================================")
		os.Exit(1)
	}

	// 创建 LLM 提供者
	providerCfg := llm.NewOpenAIProvider(llm.OpenAIConfig{
		APIKey:  finalAPIKey,
		BaseURL: finalBaseURL,
		Model:   finalModel,
	})

	// 创建 Agent
	ag := agent.NewAgent(providerCfg)
	ag.SetMaxSteps(50)

	// 设置系统提示词 - 始终使用动态构建的提示词，并允许自定义提示词作为补充
	finalPrompt := buildDefaultSystemPrompt()
	if *systemPrompt != "" {
		// 如果提供了自定义提示词，则将其附加到默认提示词后面
		finalPrompt = *systemPrompt + "\n\n" + finalPrompt
	}
	ag.SetSystemPrompt(finalPrompt)

	// 注册内置工具
	ag.AddTool(tools.NewCalculatorTool())
	ag.AddTool(tools.NewSystemInfoTool())
	ag.AddTool(tools.NewShellToolUnsafe())
	ag.AddTool(tools.NewFileEditTool())
	ag.AddTool(tools.NewFileWriteTool())
	ag.AddTool(tools.NewFileDiffTool())
	ag.AddTool(tools.NewFileReadTool())
	ag.AddTool(tools.NewCodeProbeTool())
	ag.AddTool(tools.NewCodeStatsTool())

	// 初始化会话存储
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("警告: 无法获取用户主目录: %v\n", err)
		homeDir = "."
	}
	store, err := session.NewStore(filepath.Join(homeDir, ".lite-agent", "sessions"))
	if err != nil {
		fmt.Printf("警告: 初始化会话存储失败: %v\n", err)
	}

	// 会话恢复逻辑
	var currentSession *session.Session
	if store != nil {
		switch {
		case *newSession:
			currentSession = session.NewSession()
		case *sessionID != "":
			loaded, err := store.Load(*sessionID)
			if err != nil {
				fmt.Printf("❌ 加载会话 %s 失败: %v\n", *sessionID, err)
				os.Exit(1)
			}
			currentSession = loaded
			ag.SetMemory(currentSession.Messages)
			fmt.Printf("📂 已恢复会话 %s（%d 条消息）\n", currentSession.ID, currentSession.MessageCount)
		default:
			latest, err := store.Latest()
			if err == nil && latest != nil {
				currentSession = latest
				ag.SetMemory(currentSession.Messages)
				fmt.Printf("📂 已恢复会话 %s（%d 条消息）\n", currentSession.ID, currentSession.MessageCount)
			} else {
				currentSession = session.NewSession()
			}
		}
	} else {
		currentSession = session.NewSession()
	}

	// saveSession 保存当前会话（忽略错误，仅打印警告）
	saveSession := func() {
		if store == nil {
			return
		}
		currentSession.SetMessages(ag.GetMemory())
		if err := store.Save(currentSession); err != nil {
			fmt.Printf("警告: 保存会话失败: %v\n", err)
		}
	}

	// 注册信号处理，Ctrl+C 时尽力保存
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n👋 收到退出信号，正在保存会话...")
		saveSession()
		os.Exit(0)
	}()

	// 显示启动信息
	fmt.Println("=================================")
	fmt.Println("     Go AI Agent 学习框架")
	fmt.Println("=================================")
	fmt.Println()
	fmt.Printf("📡 API: %s\n", finalBaseURL)
	fmt.Printf("🤖 Model: %s\n", finalModel)
	fmt.Printf("📝 System Prompt: %s\n", truncatePrompt(finalPrompt, 50))
	if *stream {
		fmt.Println("⚡ 流式输出: 已启用")
	}
	fmt.Println()
	fmt.Println("已加载工具:")
	fmt.Println("  - calculator   : 数学计算")
	fmt.Println("  - system_info  : 系统信息")
	fmt.Println("  - shell        : Shell 命令执行")
	fmt.Println("  - file_edit    : 文件编辑")
	fmt.Println("  - file_write   : 文件写入")
	fmt.Println("  - file_diff    : 文件比较")
	fmt.Println("  - file_read    : 文件读取")
	fmt.Println("  - code_probe   : 项目结构探查")
	fmt.Println("  - code_stats   : 代码行数统计")
	fmt.Println()
	fmt.Println("输入 'quit' 或 'exit' 退出")
	fmt.Println("输入 'prompt' 查看完整系统提示词")
	fmt.Println("输入 'sessions' 查看历史会话")
	fmt.Println("输入 'new' 开始新会话")
	fmt.Println("输入 'load <id>' 加载历史会话")
	fmt.Println("输入 'delete <id>' 删除历史会话")
	fmt.Printf("💾 当前会话: %s\n", currentSession.ID)
	fmt.Println("=================================")
	fmt.Println()

	// 交互式对话
	reader := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	for {
		fmt.Print("👤 You: ")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		// 命令路由
		switch {
		case input == "quit" || input == "exit":
			saveSession()
			fmt.Println("👋 再见!")
			return

		case input == "prompt":
			fmt.Println("📝 当前系统提示词:")
			fmt.Println("---")
			fmt.Println(finalPrompt)
			fmt.Println("---")
			continue

		case input == "sessions":
			if store == nil {
				fmt.Println("会话存储未初始化")
				continue
			}
			metas, err := store.List()
			if err != nil {
				fmt.Printf("读取会话列表失败: %v\n", err)
				continue
			}
			if len(metas) == 0 {
				fmt.Println("暂无历史会话")
				continue
			}
			fmt.Println("📋 历史会话：")
			fmt.Printf("  %-20s %-20s %-6s %s\n", "ID", "时间", "消息数", "预览")
			for _, m := range metas {
				marker := "  "
				if m.ID == currentSession.ID {
					marker = "* "
				}
				// 从 RFC3339 中提取可读时间
				displayTime := m.UpdatedAt
				if len(displayTime) >= 16 {
					displayTime = displayTime[:16]
				}
				fmt.Printf("%s%-20s %-20s %-6d %s\n", marker, m.ID, displayTime, m.MessageCount, m.Preview)
			}
			continue

		case input == "new":
			saveSession()
			currentSession = session.NewSession()
			ag.SetMemory(nil)
			fmt.Printf("✨ 已创建新会话: %s\n", currentSession.ID)
			continue

		case strings.HasPrefix(input, "load "):
			targetID := strings.TrimSpace(strings.TrimPrefix(input, "load "))
			if targetID == "" {
				fmt.Println("用法: load <session-id>")
				continue
			}
			if store == nil {
				fmt.Println("会话存储未初始化")
				continue
			}
			loaded, err := store.Load(targetID)
			if err != nil {
				fmt.Printf("加载会话失败: %v\n", err)
				continue
			}
			saveSession()
			currentSession = loaded
			ag.SetMemory(currentSession.Messages)
			fmt.Printf("📂 已加载会话 %s（%d 条消息）\n", currentSession.ID, currentSession.MessageCount)
			continue

		case strings.HasPrefix(input, "delete "):
			targetID := strings.TrimSpace(strings.TrimPrefix(input, "delete "))
			if targetID == "" {
				fmt.Println("用法: delete <session-id>")
				continue
			}
			if store == nil {
				fmt.Println("会话存储未初始化")
				continue
			}
			if targetID == currentSession.ID {
				fmt.Println("不能删除当前正在使用的会话")
				continue
			}
			if err := store.Delete(targetID); err != nil {
				fmt.Printf("删除会话失败: %v\n", err)
				continue
			}
			fmt.Printf("🗑️  已删除会话: %s\n", targetID)
			continue
		}

		// 运行 Agent
		fmt.Print("🤖 Agent: ")
		if *stream {
			// 流式模式：实时逐字输出，同时统计行数用于后续清除
			lineCount := 1 // "🤖 Agent: " 占第一行
			isFirstSegment := true

			renderer, _ := glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
			)

			// clearAndRender 清除当前流式原文并用 glamour 渲染替换
			clearAndRender := func(content string) {
				if content == "" {
					return
				}
				if rendered, err := renderer.Render(content); err == nil {
					fmt.Print("\r")
					if lineCount > 1 {
						fmt.Printf("\033[%dA", lineCount-1)
					}
					fmt.Print("\033[J")
					if isFirstSegment {
						fmt.Print("🤖 Agent: ")
						isFirstSegment = false
					}
					fmt.Print(rendered)
				}
				// 重置行计数，为下一轮流式或后续工具输出做准备
				lineCount = 0
			}

			response, err := ag.RunStream(ctx, input,
				// onChunk: 实时输出文本片段
				func(chunk string) {
					fmt.Print(chunk)
					lineCount += strings.Count(chunk, "\n")
				},
				// onFlush: tool call 执行前，清屏+渲染当前累积内容
				clearAndRender,
			)
			if err != nil {
				fmt.Printf("\n错误: %v\n", err)
			} else {
				// 最终结果也做一次清屏+渲染
				clearAndRender(response)
				fmt.Println()
			}
		} else {
			// 非流式模式：等待完整响应后输出
			response, err := ag.Run(ctx, input)
			if err != nil {
				fmt.Printf("错误: %v\n", err)
			} else {
				renderer, _ := glamour.NewTermRenderer(
					glamour.WithAutoStyle(),
				)
				out, _ := renderer.Render(response)
				fmt.Println(out)
			}
		}

		// 每轮对话后自动保存
		saveSession()

		fmt.Println()
	}
}

// truncatePrompt 截断提示词用于显示
func truncatePrompt(prompt string, maxLen int) string {
	if len(prompt) <= maxLen {
		return prompt
	}
	return prompt[:maxLen] + "..."
}
