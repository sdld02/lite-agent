//go:build windows

package instance

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsLock struct {
	f    *os.File
	path string
	ov   *windows.Overlapped
}

func (l *windowsLock) Path() string { return l.path }

func (l *windowsLock) Release() error {
	if l.f == nil {
		return nil
	}
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, l.ov)
	_ = l.f.Close()
	_ = os.Remove(l.path)
	l.f = nil
	return nil
}

// Acquire 以非阻塞方式尝试获取 path 处的独占文件锁（LockFileEx）。
func Acquire(path string) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("创建锁目录失败: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件失败: %w", err)
	}

	ov := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ov,
	)
	if err != nil {
		_ = f.Close()
		// 竞争失败统一返回 ErrLocked（Windows 返回 ERROR_LOCK_VIOLATION）
		return nil, ErrLocked
	}

	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)

	return &windowsLock{f: f, path: path, ov: ov}, nil
}
