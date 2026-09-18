// Package logging 提供统一日志初始化：文件输出 + 大小轮转（lumberjack）。
//
// 设计要点：
//   - 复用标准库 log（项目内大量 log.Printf），仅重定向输出目标。
//   - 服务化后（launchd/systemd/Windows Service）无可靠 stdout，
//     因此文件日志是主渠道；交互式运行时可选叠加 stderr。
//   - 轮转策略由 appconfig.LogConfig 控制（大小 / 保留份数）。
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"

	"lite-agent/internal/appconfig"
)

// Setup 初始化全局标准 logger。
//
// interactive 为 true 时（CLI/前台调试）会同时输出到 stderr，保证终端可见；
// 服务模式下仅写文件，避免依赖平台对 stdout/stderr 的重定向。
//
// 返回值可用于在退出前关闭底层 writer（lumberjack 会 flush）。
func Setup(cfg appconfig.LogConfig, interactive bool) (io.Closer, error) {
	var (
		fileWriter io.Writer
		closer     io.Closer
	)

	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0700); err != nil {
			return nil, fmt.Errorf("创建日志目录失败: %w", err)
		}
		lj := &lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSizeMB,  // 单文件上限（MB）
			MaxBackups: cfg.MaxBackups, // 保留历史文件数
			Compress:   true,           // 旧日志 gzip 压缩
			LocalTime:  true,
		}
		fileWriter = lj
		closer = lj
	}

	// 决定最终输出目标
	var out io.Writer
	switch {
	case fileWriter != nil && interactive:
		out = io.MultiWriter(fileWriter, os.Stderr)
	case fileWriter != nil:
		out = fileWriter
	default:
		out = os.Stderr
	}

	log.SetOutput(out)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return closer, nil
}
