//go:build windows

package service

import (
	"fmt"
	"os/exec"
	"strings"
)

// Windows 用户级使用「计划任务（Task Scheduler）」，登录时触发，无需管理员权限；
// 系统级仍交由 kardianos/service 走 SCM 服务。
const taskName = "LiteAgent"

// runPlatform：计划任务直接执行 `lite-agent service run`，运行期无需特殊处理。
func runPlatform(prog Program, opt Options) (bool, error) {
	return false, nil
}

// controlPlatform 处理 Windows 用户级的 install/uninstall/start/stop/restart。
// 返回 handled=false 表示交由 kardianos/service（系统级 SCM）处理。
func controlPlatform(action string, opt Options) (bool, error) {
	if opt.Level != LevelUser {
		return false, nil
	}
	switch action {
	case "install":
		return true, taskCreate(opt)
	case "uninstall":
		return true, taskDelete()
	case "start":
		return true, taskRun()
	case "stop":
		return true, taskEnd()
	case "restart":
		_ = taskEnd() // 未运行时忽略错误
		return true, taskRun()
	}
	return false, nil
}

// statusPlatform 查询计划任务状态（仅 Windows 用户级）。
func statusPlatform(opt Options) (bool, string, error) {
	if opt.Level != LevelUser {
		return false, "", nil
	}
	out, err := exec.Command("schtasks", "/Query", "/TN", taskName).CombinedOutput()
	if err != nil {
		return true, "not-installed", nil
	}
	// 输出中 "Running" 表示正在运行
	if strings.Contains(string(out), "Running") {
		return true, "running", nil
	}
	return true, "stopped", nil
}

func taskCreate(opt Options) error {
	exe := opt.resolvesExecutable()
	args := "service run"
	if opt.ConfigPath != "" {
		args += " --config " + opt.ConfigPath
	}
	tr := fmt.Sprintf(`"%s" %s`, exe, args)

	cmd := exec.Command("schtasks", "/Create",
		"/TN", taskName,
		"/TR", tr,
		"/SC", "ONLOGON", // 登录时触发
		"/RL", "LIMITED", // 普通权限
		"/F", // 覆盖同名任务
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("创建计划任务失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func taskDelete() error {
	cmd := exec.Command("schtasks", "/Delete", "/TN", taskName, "/F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("删除计划任务失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func taskRun() error {
	cmd := exec.Command("schtasks", "/Run", "/TN", taskName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("启动计划任务失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func taskEnd() error {
	cmd := exec.Command("schtasks", "/End", "/TN", taskName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("停止计划任务失败: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
