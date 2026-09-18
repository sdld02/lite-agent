package main

import (
	"flag"
	"fmt"
	"os"

	"lite-agent/internal/appconfig"
	"lite-agent/internal/service"
)

// serviceRunMode 标记当前进程是否由服务管理器（launchd/systemd/SCM/计划任务）托管运行。
var serviceRunMode bool

const serviceUsage = `lite-agent service <command> [options]

命令:
  install      安装为开机自启服务
  uninstall    卸载服务
  start        启动服务
  stop         停止服务
  restart      重启服务
  status       查看服务状态
  run          前台运行（由服务管理器调用，通常无需手动执行）
  platform     显示当前平台使用的服务管理器

选项:
  --level=user|system   运行级别
                        user   = 登录后自动启动（默认，免特权）
                        system = 开机即启动（Unix 需 root，Windows 用 SCM 服务）
  --config=PATH         配置文件路径（默认 ~/.lite-agent/config.json）
  --workdir=PATH        服务工作目录
  --name=NAME           服务名（默认 lite-agent）

示例:
  lite-agent service install --level=user
  sudo lite-agent service install --level=system
  lite-agent service status
  lite-agent service stop
`

// handleServiceSubcommand 处理 `lite-agent service <action> [...]`。
//
// 返回值：
//   - true  : 命令已处理完毕，main 应直接返回（控制类命令）
//   - false : 这是 `run` 模式，需继续执行正常的服务启动流程，
//     且 os.Args 已被重写为等价的服务启动参数。
func handleServiceSubcommand(rest []string) bool {
	if len(rest) == 0 {
		fmt.Print(serviceUsage)
		os.Exit(2)
	}

	action := rest[0]
	args := rest[1:]

	fs := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	levelStr := fs.String("level", "user", "运行级别: user|system")
	configPath := fs.String("config", "", "配置文件路径")
	workDir := fs.String("workdir", "", "工作目录")
	name := fs.String("name", service.DefaultName, "服务名")
	_ = fs.Parse(args)

	// platform 动作无需级别等参数
	if action == "platform" {
		fmt.Println(service.Platform())
		return true
	}

	level, err := service.ParseLevel(*levelStr)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(2)
	}

	opt := service.Options{
		Name:       *name,
		Level:      level,
		ConfigPath: *configPath,
		WorkDir:    *workDir,
	}

	switch action {
	case "run":
		// 由服务管理器调用：读取配置决定运行模式并重写 os.Args，
		// 随后由 main 正常流程启动业务，并通过 kardianos 包裹。
		serviceRunMode = true

		homeDir, _ := os.UserHomeDir()
		mode := appconfig.ModeServer
		if store, err := appconfig.Open(*configPath, homeDir); err == nil {
			mode = store.Get().Runtime.Mode
		}

		newArgs := []string{os.Args[0]}
		if mode == appconfig.ModeTelegram {
			newArgs = append(newArgs, "-telegram")
		} else {
			// server / both / 其他：均以 server 模式启动（both 会自动拉起 Telegram）
			newArgs = append(newArgs, "-server")
		}
		if *configPath != "" {
			newArgs = append(newArgs, "-config", *configPath)
		}
		os.Args = newArgs
		return false

	case "install", "uninstall", "start", "stop", "restart":
		if err := service.Control(action, opt); err != nil {
			fmt.Printf("❌ %s 失败: %v\n", action, err)
			os.Exit(1)
		}
		fmt.Printf("✅ %s 成功（级别: %s，服务名: %s）\n", action, level, opt.Name)
		if action == "install" {
			fmt.Println("💡 请确认 ~/.lite-agent/config.json 中：")
			fmt.Println("   - llm.apiKey 已配置（否则服务会因缺少 Key 而反复重启）")
			fmt.Println("   - runtime.mode 设为 server / telegram / both（服务按此模式启动）")
		}
		return true

	case "status":
		st, err := service.Status(opt)
		if err != nil {
			fmt.Printf("❌ 查询状态失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("服务状态: %s\n", st)
		return true

	default:
		fmt.Printf("❌ 未知动作: %s\n\n", action)
		fmt.Print(serviceUsage)
		os.Exit(2)
	}
	return true
}
