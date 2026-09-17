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
	"time"

	"lite-agent/agent"
	"lite-agent/bot"
	"lite-agent/internal/strutil"
	"lite-agent/llm"
	"lite-agent/server"
	"lite-agent/session"
	"lite-agent/tools"
	agentpkg "lite-agent/tools/agent"
	"lite-agent/tools/skill"

	"github.com/charmbracelet/glamour"
)

// ANSI 颜色常量
const (
	colorGray  = "\033[90m" // 灰色（亮黑色），用于推理内容展示
	colorReset = "\033[0m"  // 颜色重置
)

// 支持的 LLM 提供者预设配置
var llmProviders = map[string]struct {
	baseURL string
	model   string
}{
	"openai":   {"https://api.openai.com/v1", "gpt-4o"},
	"deepseek": {"https://api.deepseek.com/v1", "deepseek-flash"},
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

// buildDefaultSystemPrompt 构建默认系统提示词（包含动态系统信息和技能列表）
func buildDefaultSystemPrompt(toolsSection, skillsPrompt string) string {
	sysInfo := getSystemInfo()

	basePrompt := fmt.Sprintf(`你是一个智能助手，运行在以下系统环境中：

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
%s

%s

## 行为准则
1. 当用户请求需要使用工具时，请调用相应的工具来完成任务
2. 如果用户的问题不需要使用工具，请直接回答
3. 执行 shell 命令时，注意当前操作系统是 %s，使用适合该系统的命令
4. 当用户引用"斜杠命令"或"/<something>"时（如 /commit、/review-pr），应调用 skill 工具
5. 当需要在执行过程中了解用户偏好、澄清歧义或让用户做选择时，使用 ask_user_question 工具发起多选题
6. 请用中文回复用户`,
		sysInfo["os"],
		sysInfo["arch"],
		sysInfo["cpus"],
		sysInfo["version"],
		sysInfo["hostname"],
		sysInfo["user"],
		sysInfo["homeDir"],
		sysInfo["workDir"],
		toolsSection,
		skillsPrompt,
		sysInfo["os"],
	)

	return basePrompt
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
	serverMode := flag.Bool("server", false, "以 WebSocket 服务模式启动（常驻后台）")
	serverAddr := flag.String("addr", ":9090", "WebSocket 服务监听地址")
	telegramMode := flag.Bool("telegram", false, "以 Telegram Bot 模式启动")
	telegramToken := flag.String("token", "", "Telegram Bot Token（也可通过 TELEGRAM_BOT_TOKEN 环境变量设置）")
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

	// 获取用户主目录（TaskManager 和 Session 共用）
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("警告: 无法获取用户主目录: %v\n", err)
		homeDir = "."
	}

	// 初始化任务管理器（多 Agent 支持的基础设施）
	taskMgr := tools.InitTaskManager(homeDir)

	// 创建 Agent
	ag := agent.NewAgent(providerCfg)
	ag.SetMaxSteps(50)

	// 创建工具注册表（子Agent系统需要）
	registry := tools.NewToolRegistry()
	cat := toolCatalog(providerCfg)
	baseSpecs := baseToolSpecs(cat)
	registerBaseTools(registry, baseSpecs)

	// 获取项目根目录（用于加载项目级技能）
	projectRoot, _ := os.Getwd()

	// 初始化 MCP 管理器（按需加载，不启动服务器）
	tools.InitMCPManager(homeDir, projectRoot)
	if mgr := tools.GetMCPManager(); mgr != nil && mgr.HasServers() {
		registry.Register("mcp", func() agent.Tool { return tools.NewMCPTool(mgr) })
	}

	// 创建 Skill 工具（需要在 registry 之后，因为 fork 模式需要 registry）
	skillTool := tools.NewSkillTool(homeDir, projectRoot, registry, providerCfg)
	registry.Register("skill", func() agent.Tool { return skillTool })

	// 设置系统提示词 - 始终使用动态构建的提示词，并允许自定义提示词作为补充
	skillsPrompt := skill.FormatSkillsPrompt(skillTool.GetSkills(), 3000)
	mcpPrompt := tools.FormatMCPServersPrompt(tools.GetMCPManager())
	finalPrompt := buildDefaultSystemPrompt(toolPromptSection(cat), skillsPrompt)
	if mcpPrompt != "" {
		finalPrompt += mcpPrompt
	}
	if *systemPrompt != "" {
		// 如果提供了自定义提示词，则将其附加到默认提示词后面
		finalPrompt = *systemPrompt + "\n\n" + finalPrompt
	}
	ag.SetSystemPrompt(finalPrompt)

	// 装配内置工具（基础工具 + agent/skill/mcp 三个特殊工具）
	addBaseTools(ag, baseSpecs)
	// Agent 子Agent工具
	ag.AddTool(tools.NewAgentTool(registry, providerCfg))
	// Skill 技能工具（与注册表共享同一实例）
	ag.AddTool(skillTool)
	// MCP 工具
	if mgr := tools.GetMCPManager(); mgr != nil && mgr.HasServers() {
		ag.AddTool(tools.NewMCPTool(mgr))
	}

	// 初始化会话存储
	store, err := session.NewStore(filepath.Join(homeDir, ".lite-agent", "sessions"))
	if err != nil {
		fmt.Printf("警告: 初始化会话存储失败: %v\n", err)
	}

	// === Server 模式分支 ===
	if *serverMode {
		// 构建工具工厂列表（为每个连接创建独立工具实例）
		// 基础工具来自统一规格表，再补充 skill / mcp 两个每连接特殊工厂
		toolFactories := baseToolFactories(baseSpecs)
		// Skill 技能工具（每个连接独立实例，共享 filesystem）
		toolFactories = append(toolFactories, func() agent.Tool {
			workDir, _ := os.Getwd()
			return tools.NewSkillTool(homeDir, workDir, registry, providerCfg)
		})
		// MCP 工具（共享全局管理器）
		toolFactories = append(toolFactories, func() agent.Tool {
			if mgr := tools.GetMCPManager(); mgr != nil {
				return tools.NewMCPTool(mgr)
			}
			return nil
		})
		// 注：agent 子Agent工具需要独立的 registry，在 handler 中为每个连接创建

		// 创建 WebSocket 服务（注册表用于子 Agent 工具）
		srv := server.NewServer(*serverAddr, store, registry, providerCfg, finalPrompt, 50, toolFactories, taskMgr)

		// 注册信号处理：优雅关闭
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			fmt.Println("\n🛑 收到退出信号，正在关闭服务...")
			// 清理 MCP 连接
			if mgr := tools.GetMCPManager(); mgr != nil {
				mgr.Shutdown()
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				fmt.Printf("关闭服务出错: %v\n", err)
			}
			os.Exit(0)
		}()

		// 启动服务（阻塞）
		fmt.Println("=================================")
		fmt.Println("  Go AI Agent - WebSocket 服务")
		fmt.Println("=================================")
		fmt.Println()
		fmt.Printf("📡 API: %s\n", finalBaseURL)
		fmt.Printf("🤖 Model: %s\n", finalModel)
		fmt.Printf("🌐 服务地址: ws://%s/ws\n", *serverAddr)
		fmt.Printf("❤️  健康检查: http://%s/health\n", *serverAddr)
		fmt.Println()
		printToolBanner(cat, true)
		fmt.Println("=================================")
		fmt.Println()

		if err := srv.Start(); err != nil {
			fmt.Printf("服务启动失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// === Telegram Bot 模式分支 ===
	if *telegramMode {
		// 确定 Bot Token
		botToken := *telegramToken
		if botToken == "" {
			botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		}
		if botToken == "" {
			fmt.Println("❌ 请设置 Telegram Bot Token")
			fmt.Println()
			fmt.Println("方式一：环境变量")
			fmt.Println("  export TELEGRAM_BOT_TOKEN='your-bot-token'")
			fmt.Println()
			fmt.Println("方式二：命令行参数")
			fmt.Println("  ./lite-agent -telegram -key=xxx -token=your-bot-token")
			fmt.Println()
			fmt.Println("获取 Token：在 Telegram 中找到 @BotFather，发送 /newbot 创建机器人")
			os.Exit(1)
		}

		botInstance, err := bot.New(bot.Config{
			Token:        botToken,
			SystemPrompt: finalPrompt,
			MaxSteps:     50,
			Registry:     registry,
			ProviderCfg:  providerCfg,
		})
		if err != nil {
			fmt.Printf("❌ 创建 Telegram Bot 失败: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("=================================")
		fmt.Println("  Go AI Agent - Telegram Bot")
		fmt.Println("=================================")
		fmt.Println()
		fmt.Printf("📡 API: %s\n", finalBaseURL)
		fmt.Printf("🤖 Model: %s\n", finalModel)
		fmt.Println()
		printToolBanner(cat, false)
		fmt.Println("=================================")
		fmt.Println()

		if err := botInstance.Start(); err != nil {
			fmt.Printf("❌ Telegram Bot 运行失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// === 交互式 CLI 模式 ===

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
		if mgr := tools.GetMCPManager(); mgr != nil {
			mgr.Shutdown()
		}
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
	printToolBanner(cat, true)
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

	// CLI 模式的 QuestionHandler：从 stdin 读取答案
	ctx = tools.SetQuestionHandler(ctx, func(questions []tools.Question) (map[string]string, error) {
		fmt.Println("\n📋 Claude 正在向您提问：")
		fmt.Println(strings.Repeat("─", 60))
		for i, q := range questions {
			fmt.Printf("\n%d. [%s] %s\n", i+1, q.Header, q.Question)
			for j, opt := range q.Options {
				fmt.Printf("   %c) %s — %s\n", 'A'+j, opt.Label, opt.Description)
			}
			if q.MultiSelect {
				fmt.Println("   (可多选，用逗号分隔)")
			}
		}
		fmt.Println(strings.Repeat("─", 60))

		answers := make(map[string]string)
		for _, q := range questions {
			if q.MultiSelect {
				fmt.Printf(">>> %s (多选，输入字母如 A,C): ", q.Header)
			} else {
				fmt.Printf(">>> %s (单选，输入字母): ", q.Header)
			}

			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(answer)

			if answer == "" {
				answers[q.Question] = "(未回答)"
				continue
			}

			// 解析用户选择的字母
			selectedLetters := strings.FieldsFunc(answer, func(r rune) bool {
				return r == ',' || r == ' ' || r == '，'
			})
			var selectedLabels []string
			for _, letter := range selectedLetters {
				letter = strings.TrimSpace(strings.ToUpper(letter))
				if len(letter) == 1 && letter[0] >= 'A' {
					idx := int(letter[0] - 'A')
					if idx >= 0 && idx < len(q.Options) {
						selectedLabels = append(selectedLabels, q.Options[idx].Label)
					}
				}
			}
			if len(selectedLabels) > 0 {
				answers[q.Question] = strings.Join(selectedLabels, ", ")
			} else {
				answers[q.Question] = answer
			}
		}
		fmt.Println()
		return answers, nil
	})

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
			if mgr := tools.GetMCPManager(); mgr != nil {
				mgr.Shutdown()
			}
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
			reasoningActive := false // 追踪 reasoning/content 阶段切换
			progressActive := false  // 追踪进度行是否在显示

			renderer, _ := glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
			)

			// clearAndRender 清除当前流式原文并用 glamour 渲染替换
			clearAndRender := func(content string) {
				if content == "" {
					return
				}
				// 确保颜色已重置
				if reasoningActive {
					fmt.Print(colorReset)
					reasoningActive = false
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

			response, err := ag.RunStream(ctx, input, func(event agent.StreamEvent) {
				switch event.Type {
				case agent.EventContent:
					if progressActive {
						fmt.Print("\r\033[K")
						progressActive = false
					}
					if reasoningActive {
						fmt.Print(colorReset)
						fmt.Println()
						lineCount++
						reasoningActive = false
					}
					fmt.Print(event.Content)
					lineCount += strings.Count(event.Content, "\n")
				case agent.EventReasoning:
					if !reasoningActive {
						fmt.Print(colorGray)
						reasoningActive = true
					}
					fmt.Print(event.Content)
					lineCount += strings.Count(event.Content, "\n")
				case agent.EventToolCallProgress:
					progressActive = true
					fmt.Printf("\r\033[K⏳ %s: 生成参数中... %.1fKB", event.ToolName, float64(event.ArgsBytes)/1024)
				case agent.EventFlush:
					if progressActive {
						fmt.Print("\r\033[K")
						progressActive = false
					}
					clearAndRender(event.Content)
				case agent.EventToolCallStart:
					if progressActive {
						fmt.Print("\r\033[K")
						progressActive = false
					}
					fmt.Printf("\n🔧 调用工具: %s\n", event.ToolName)
					if event.ToolName == "shell" {
						if intent, ok := event.ToolArgs["intent"]; ok {
							fmt.Printf("   意图: %s\n", intent)
						}
						if cmd, ok := event.ToolArgs["command"]; ok {
							fmt.Printf("   命令: %s\n", cmd)
						}
					}
				case agent.EventToolCallEnd:
					if event.ToolResult != nil && !event.ToolResult.IsError {
						fmt.Printf("   ✅ 完成\n\n")
					} else if event.ToolResult != nil {
						fmt.Printf("   ❌ 错误: %s\n\n", event.ToolResult.Content)
					}
				}
			})
			if err != nil {
				if reasoningActive {
					fmt.Print(colorReset)
				}
				if progressActive {
					fmt.Print("\r\033[K")
				}
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
				// 展示推理内容（如果有）
				if mem := ag.GetMemory(); len(mem) > 0 {
					last := mem[len(mem)-1]
					if last.Role == "assistant" && last.ReasoningContent != "" {
						fmt.Printf("%s%s%s\n\n", colorGray, last.ReasoningContent, colorReset)
					}
				}
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

// truncatePrompt 截断提示词用于显示（UTF-8 安全）
func truncatePrompt(prompt string, maxLen int) string {
	return strutil.TruncateRunes(prompt, maxLen, "...")
}

// ============================================================================
// 工具装配与展示的单一数据源
//
// 内置工具的「注册 / 装配 / 每连接工厂 / 系统提示词 / 启动横幅」全部由此处的
// toolCatalog 派生，增删工具只需改这一处，避免多处硬编码导致的不同步。
// ============================================================================

// toolRole 描述工具在装配中的角色（不同角色的装配方式不同）。
type toolRole int

const (
	roleBase  toolRole = iota // 基础工具：统一注册 / 装配 / 每连接工厂
	roleAgent                 // 子Agent工具：仅加入主 Agent（避免子 Agent 无限递归）
	roleSkill                 // 技能工具：CLI/Telegram 共享实例，Server 每连接独立实例
	roleMCP                   // MCP 工具：仅在配置了 MCP 服务器时启用
)

// toolSpec 基础工具的构造规格（供注册 / 装配 / 工厂使用）。
type toolSpec struct {
	name    string
	factory func() agent.Tool
}

// toolMeta 单个工具的元数据：装配角色 + 展示描述。
type toolMeta struct {
	name       string
	role       toolRole
	factory    func() agent.Tool // 仅 roleBase 使用
	promptDesc string            // 系统提示词描述（空表示不写入提示词）
	banner     string            // 启动横幅整行（空表示不在横幅展示，如被 task_* 聚合）
}

// toolCatalog 返回全部工具的元数据（按展示顺序，即提示词与横幅的顺序）。
// providerCfg 供 web_fetch 等需要 LLM 的工具使用。
func toolCatalog(providerCfg agent.LLMProvider) []toolMeta {
	return []toolMeta{
		{name: "calculator", role: roleBase, factory: func() agent.Tool { return tools.NewCalculatorTool() },
			promptDesc: "执行数学计算", banner: "  - calculator   : 数学计算"},
		{name: "system_info", role: roleBase, factory: func() agent.Tool { return tools.NewSystemInfoTool() },
			promptDesc: "获取系统信息", banner: "  - system_info  : 系统信息"},
		{name: "current_time", role: roleBase, factory: func() agent.Tool { return tools.NewTimeTool() },
			promptDesc: "获取当前日期和时间", banner: "  - current_time : 当前日期和时间"},
		{name: "shell", role: roleBase, factory: func() agent.Tool { return tools.NewShellToolUnsafe() },
			promptDesc: "执行系统命令", banner: "  - shell        : Shell 命令执行"},
		{name: "file_edit", role: roleBase, factory: func() agent.Tool { return tools.NewFileEditTool() },
			promptDesc: "编辑文件内容（精确字符串替换）", banner: "  - file_edit    : 文件编辑"},
		{name: "file_write", role: roleBase, factory: func() agent.Tool { return tools.NewFileWriteTool() },
			promptDesc: "写入文件内容（创建或覆盖文件）", banner: "  - file_write   : 文件写入"},
		{name: "file_diff", role: roleBase, factory: func() agent.Tool { return tools.NewFileDiffTool() },
			promptDesc: "比较两个文件的差异", banner: "  - file_diff    : 文件比较"},
		{name: "file_read", role: roleBase, factory: func() agent.Tool { return tools.NewFileReadTool() },
			promptDesc: "读取文件内容", banner: "  - file_read    : 文件读取"},
		{name: "code_probe", role: roleBase, factory: func() agent.Tool { return tools.NewCodeProbeTool() },
			promptDesc: "探查项目结构（支持 summary/structure/flat/grouped/tree/recent 模式）", banner: "  - code_probe   : 项目结构探查"},
		{name: "code_stats", role: roleBase, factory: func() agent.Tool { return tools.NewCodeStatsTool() },
			promptDesc: "统计代码行数（支持按语言分组统计）", banner: "  - code_stats   : 代码行数统计"},
		{name: "lsp", role: roleBase, factory: func() agent.Tool { return tools.NewLSPTool() },
			promptDesc: "LSP 代码智能（跳转定义、查找引用、悬停文档、文档符号、工作区符号、调用层次等）", banner: "  - lsp          : LSP 代码智能"},
		{name: "agent", role: roleAgent,
			promptDesc: "启动子Agent处理复杂的多步骤任务（支持 general-purpose、Explore（只读搜索）、Plan（只读规划）等类型）", banner: "  - agent        : 子Agent系统 (general-purpose/Explore/Plan)"},
		{name: "task_create", role: roleBase, factory: func() agent.Tool { return tools.NewTaskCreateTool() },
			promptDesc: "创建任务", banner: "  - task_*       : 任务管理 (create/update/list/get)"},
		{name: "task_update", role: roleBase, factory: func() agent.Tool { return tools.NewTaskUpdateTool() },
			promptDesc: "更新任务状态"},
		{name: "task_list", role: roleBase, factory: func() agent.Tool { return tools.NewTaskListTool() },
			promptDesc: "列出所有任务"},
		{name: "task_get", role: roleBase, factory: func() agent.Tool { return tools.NewTaskGetTool() },
			promptDesc: "获取任务详情"},
		{name: "skill", role: roleSkill,
			promptDesc: "调用技能（斜杠命令），如 commit、review-pr、explain-code、plan 等", banner: "  - skill        : 技能系统 (commit/review-pr/explain-code/plan)"},
		{name: "ask_user_question", role: roleBase, factory: func() agent.Tool { return tools.NewAskUserQuestionTool() },
			promptDesc: "在任务执行过程中向用户提问（多选题），用于收集偏好、澄清歧义、获取决策", banner: "  - ask_user_question : 用户提问（执行中向用户发起多选题）"},
		{name: "grep", role: roleBase, factory: func() agent.Tool { return tools.NewGrepTool() },
			promptDesc: `强大的代码搜索工具（纯Go实现，零外部依赖，跨平台可用）。ALWAYS 使用 grep 工具进行文件内容搜索，NEVER 通过 shell 调用 grep/rg 等外部命令。支持正则表达式、glob过滤、三种输出模式、分页。`, banner: "  - grep              : 代码搜索（纯Go，正则/glob/三种输出模式）"},
		{name: "glob", role: roleBase, factory: func() agent.Tool { return tools.NewGlobTool() },
			promptDesc: `快速文件名模式匹配工具，支持 ** 递归匹配（如 "**/*_test.go"），返回按修改时间排序的文件列表，适用于按文件名模式查找文件`, banner: "  - glob              : 文件名匹配（纯Go，支持 ** 递归）"},
		{name: "web_fetch", role: roleBase, factory: func() agent.Tool { return tools.NewWebFetchTool(providerCfg) },
			promptDesc: "抓取指定 URL 内容并用 AI 分析，适用于阅读文档、文章等网页内容", banner: "  - web_fetch         : 网页抓取与分析"},
		{name: "web_search", role: roleBase, factory: func() agent.Tool { return tools.NewWebSearchTool() },
			promptDesc: "通过 DuckDuckGo 搜索互联网获取最新信息，返回搜索结果的标题、URL 和摘要", banner: "  - web_search        : DuckDuckGo 网页搜索"},
		{name: "mcp", role: roleMCP,
			promptDesc: `调用 MCP (Model Context Protocol) 服务器提供的工具。使用方式：先用 operation="list_tools" 查看服务器提供的工具，再用 operation="call_tool" 调用具体工具`, banner: "  - mcp               : MCP 协议工具 (按需加载)"},
	}
}

// baseToolSpecs 从工具表中筛选出基础工具（roleBase）的构造规格。
func baseToolSpecs(cat []toolMeta) []toolSpec {
	var specs []toolSpec
	for _, m := range cat {
		if m.role == roleBase && m.factory != nil {
			specs = append(specs, toolSpec{name: m.name, factory: m.factory})
		}
	}
	return specs
}

// mcpServersEnabled 返回是否配置并启用了 MCP 服务器。
func mcpServersEnabled() bool {
	mgr := tools.GetMCPManager()
	return mgr != nil && mgr.HasServers()
}

// toolPromptSection 由工具表生成系统提示词的“可用工具”清单。
// roleMCP 仅在配置了 MCP 服务器时才列出，避免提示词与实际注册不一致。
func toolPromptSection(cat []toolMeta) string {
	var lines []string
	for _, m := range cat {
		if m.promptDesc == "" {
			continue
		}
		if m.role == roleMCP && !mcpServersEnabled() {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", m.name, m.promptDesc))
	}
	return strings.Join(lines, "\n")
}

// registerBaseTools 将基础工具注册到子 Agent 工具注册表。
func registerBaseTools(reg *agentpkg.ToolRegistry, specs []toolSpec) {
	for _, s := range specs {
		reg.Register(s.name, s.factory)
	}
}

// addBaseTools 将基础工具加入主 Agent。
func addBaseTools(ag *agent.Agent, specs []toolSpec) {
	for _, s := range specs {
		ag.AddTool(s.factory())
	}
}

// baseToolFactories 将基础工具转换为 Server 的每连接工具工厂。
func baseToolFactories(specs []toolSpec) []server.ToolFactory {
	factories := make([]server.ToolFactory, 0, len(specs))
	for _, s := range specs {
		factories = append(factories, s.factory)
	}
	return factories
}

// printToolBanner 打印“已加载工具”清单（由工具表生成，含条件性的 MCP 行）。
// showSkillCount 为 true 时额外打印内置技能数量（Telegram 模式维持不打印以保持原输出）。
func printToolBanner(cat []toolMeta, showSkillCount bool) {
	fmt.Println("已加载工具:")
	for _, m := range cat {
		if m.banner == "" {
			continue
		}
		if m.role == roleMCP && !mcpServersEnabled() {
			continue
		}
		fmt.Println(m.banner)
	}
	if showSkillCount {
		fmt.Printf("  📂 内置技能: %d 个\n", len(skill.BuiltinSkills))
	}
}
