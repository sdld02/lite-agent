//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// setSysProcAttr 设置进程组属性，便于后续 tree-kill（Unix 特有）
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 终止整个进程组（Unix 特有，支持 gentle 模式的 SIGTERM → SIGKILL 两段式终止）
func killProcessGroup(proc *os.Process, gentle bool) {
	pid := proc.Pid
	if gentle {
		// 先 SIGTERM，给进程清理机会
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		time.AfterFunc(3*time.Second, func() {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
	} else {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
