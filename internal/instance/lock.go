// Package instance 提供跨进程的单实例锁，避免重复启动导致端口冲突。
//
// 实现按平台区分（Unix flock / Windows LockFileEx），对外暴露统一接口。
package instance

import (
	"errors"
	"path/filepath"
)

// ErrLocked 表示已有实例持有该锁
var ErrLocked = errors.New("another instance is already running")

// Lock 表示一个已成功获取的单实例锁
type Lock interface {
	// Path 返回锁文件路径
	Path() string
	// Release 释放锁并清理锁文件
	Release() error
}

// DefaultPath 返回默认锁文件路径 ~/.lite-agent/lite-agent-<instance>.pid
func DefaultPath(homeDir, name string) string {
	if name == "" {
		name = "default"
	}
	return filepath.Join(homeDir, ".lite-agent", "lite-agent-"+name+".pid")
}
