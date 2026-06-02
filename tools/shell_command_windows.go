//go:build windows

package tools

import (
	"os"
	"os/exec"
)

// setSysProcAttr Window 不需要设置进程组
func setSysProcAttr(cmd *exec.Cmd) {
	// 无操作：Windows 不支持 Unix 进程组
}

// killProcessGroup 终止进程（Windows 直接调用 Process.Kill）
func killProcessGroup(proc *os.Process, gentle bool) {
	_ = proc.Kill()
}
