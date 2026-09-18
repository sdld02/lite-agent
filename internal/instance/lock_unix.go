//go:build !windows

package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type unixLock struct {
	f    *os.File
	path string
}

func (l *unixLock) Path() string { return l.path }

func (l *unixLock) Release() error {
	if l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	_ = os.Remove(l.path)
	l.f = nil
	return nil
}

// Acquire 以非阻塞方式尝试获取 path 处的独占文件锁（flock）。
func Acquire(path string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("创建锁目录失败: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件失败: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("获取文件锁失败: %w", err)
	}

	// 写入当前 PID，便于排查
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)

	return &unixLock{f: f, path: path}, nil
}
