//go:build !windows
// +build !windows

package task

import (
	"os"
	"path/filepath"
	"syscall"
)

// fileLock 封装 syscall.Flock 文件锁（Unix/Linux/macOS）
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
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX)
}

func (l *fileLock) Unlock() error {
	defer l.f.Close()
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
}
