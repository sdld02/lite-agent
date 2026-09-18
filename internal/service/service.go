// Package service 封装跨平台服务生命周期管理（基于 kardianos/service）。
//
// 支持平台：
//   - macOS  : launchd（用户级 LaunchAgent / 系统级 LaunchDaemon）
//   - Linux  : systemd（--user 或系统级）
//   - Windows: SCM 服务（系统级）
//
// 运行级别通过 Options.Level 选择：user（登录后）或 system（开机即启）。
// Windows 的用户级由 platform_windows.go 以「计划任务」方式实现。
package service

import (
	"fmt"
	"os"
	"strings"
	"sync"

	kservice "github.com/kardianos/service"
)

// DefaultName 默认服务名
const DefaultName = "lite-agent"

// Level 运行级别
type Level int

const (
	// LevelUser 用户级：用户登录后自动启动（无需 root/管理员）
	LevelUser Level = iota
	// LevelSystem 系统级：开机即启（Unix 需 root，Windows 为 SCM 服务）
	LevelSystem
)

func (l Level) String() string {
	if l == LevelSystem {
		return "system"
	}
	return "user"
}

// ParseLevel 解析运行级别字符串（空值默认 user）
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "user":
		return LevelUser, nil
	case "system":
		return LevelSystem, nil
	default:
		return LevelUser, fmt.Errorf("未知的运行级别: %q（可选 user|system）", s)
	}
}

// Program 是主程序提供的业务启停接口。
// Start 必须非阻塞；Stop 应阻塞至资源释放完成。
type Program interface {
	Start() error
	Stop() error
}

// Options 服务定义参数
type Options struct {
	Name       string // 服务名，默认 lite-agent
	Level      Level  // 运行级别
	ConfigPath string // 传给 `service run` 的配置文件路径
	WorkDir    string // 服务工作目录
	Executable string // 可执行文件路径，默认取当前进程
}

func (o Options) serviceName() string {
	if o.Name == "" {
		return DefaultName
	}
	return o.Name
}

// resolvesExecutable 返回可执行文件绝对路径
func (o Options) resolvesExecutable() string {
	if o.Executable != "" {
		return o.Executable
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// kconfig 根据 Options 构造 kardianos 配置
func (o Options) kconfig() *kservice.Config {
	args := []string{"service", "run"}
	if o.ConfigPath != "" {
		args = append(args, "--config", o.ConfigPath)
	}

	c := &kservice.Config{
		Name:             o.serviceName(),
		DisplayName:      "Lite Agent",
		Description:      "Lite Agent - AI Agent Service (WebSocket + Telegram)",
		Executable:       o.resolvesExecutable(),
		Arguments:        args,
		WorkingDirectory: o.WorkDir,
		Option:           kservice.KeyValue{},
	}

	// 通用保活/自启策略
	//   macOS : KeepAlive + RunAtLoad
	//   Linux : Restart=always
	//   Windows: 服务恢复由 SCM 处理
	c.Option["KeepAlive"] = true
	c.Option["RunAtLoad"] = true
	c.Option["Restart"] = "always"

	if o.Level == LevelUser {
		c.Option["UserService"] = true
	}
	return c
}

// noopIface 用于仅需控制（install/uninstall/...）而无需运行业务的场景
type noopIface struct{}

func (noopIface) Start(s kservice.Service) error { return nil }
func (noopIface) Stop(s kservice.Service) error  { return nil }

// runner 将 Program 适配为 kardianos Interface，保证 Stop 幂等
type runner struct {
	prog Program
	once sync.Once
}

func (r *runner) Start(s kservice.Service) error {
	if r.prog == nil {
		return nil
	}
	return r.prog.Start()
}

func (r *runner) Stop(s kservice.Service) error {
	if r.prog == nil {
		return nil
	}
	var err error
	r.once.Do(func() { err = r.prog.Stop() })
	return err
}

// build 创建底层 kservice.Service
func build(prog Program, opt Options) (kservice.Service, error) {
	var iface kservice.Interface
	if prog == nil {
		iface = noopIface{}
	} else {
		iface = &runner{prog: prog}
	}
	return kservice.New(iface, opt.kconfig())
}

// Control 执行控制动作：install | uninstall | start | stop | restart
func Control(action string, opt Options) error {
	// 平台特化（如 Windows 用户级 → 计划任务）
	if handled, err := controlPlatform(action, opt); handled {
		return err
	}
	svc, err := build(nil, opt)
	if err != nil {
		return err
	}
	switch action {
	case "install":
		return svc.Install()
	case "uninstall":
		return svc.Uninstall()
	case "start":
		return svc.Start()
	case "stop":
		return svc.Stop()
	case "restart":
		return svc.Restart()
	default:
		return fmt.Errorf("未知动作: %q", action)
	}
}

// Status 查询服务状态：running | stopped | not-installed | unknown
func Status(opt Options) (string, error) {
	if handled, status, err := statusPlatform(opt); handled {
		return status, err
	}
	svc, err := build(nil, opt)
	if err != nil {
		return "", err
	}
	st, err := svc.Status()
	if err != nil {
		if err == kservice.ErrNotInstalled {
			return "not-installed", nil
		}
		return "", err
	}
	switch st {
	case kservice.StatusRunning:
		return "running", nil
	case kservice.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}

// Run 以服务方式运行（阻塞），由服务管理器调用。
func Run(prog Program, opt Options) error {
	// Windows 用户级走「计划任务」，由平台文件实现
	if handled, err := runPlatform(prog, opt); handled {
		return err
	}
	svc, err := build(prog, opt)
	if err != nil {
		return err
	}
	return svc.Run()
}

// Platform 返回底层服务管理器名称（launchd/systemd/windows）
func Platform() string { return kservice.Platform() }

// Interactive 返回是否运行在交互式终端（而非服务管理器下）
func Interactive() bool { return kservice.Interactive() }
