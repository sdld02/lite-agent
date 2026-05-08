//go:build windows
// +build windows

package task

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// fileLock 封装 Windows LockFileEx 文件锁
type fileLock struct {
	f *os.File
}

func newFileLock(path string) (*fileLock, error) {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755) //nolint:errcheck

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) Lock() error {
	// LOCKFILE_EXCLUSIVE_LOCK = 2, 排他锁，与 Unix LOCK_EX 语义一致
	return windows.LockFileEx(windows.Handle(l.f.Fd()), 2, 0, 1, 0, nil)
}

func (l *fileLock) Unlock() error {
	defer l.f.Close()
	// unlock 整个文件（低32位和高32位都设为最大值）
	return windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, nil)
}
