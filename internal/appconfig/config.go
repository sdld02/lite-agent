// Package appconfig 负责 lite-agent 的持久化配置（~/.lite-agent/config.json）。
//
// 设计要点：
//   - 单文件 JSON，跨平台一致（Windows 走 %USERPROFILE%\.lite-agent）。
//   - 原子写入（临时文件 + rename），目录 0700、文件 0600。
//   - Store 提供并发安全的读取与「读-改-写」更新，供 server 层回写配置使用。
//   - 优先级合并由调用方（main）完成：命令行 > 环境变量 > config.json > 内置默认。
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DirName 用户级配置目录名（位于用户主目录下）
	DirName = ".lite-agent"
	// FileName 配置文件名
	FileName = "config.json"
	// CurrentVersion 当前配置结构版本，便于未来迁移
	CurrentVersion = 1
)

// 运行模式取值
const (
	ModeCLI      = "cli"      // 交互式命令行（默认，兼容现状）
	ModeServer   = "server"   // 仅 WebSocket 服务
	ModeTelegram = "telegram" // 仅 Telegram Bot
	ModeBoth     = "both"     // 单进程同时托管 Server + Telegram
)

// Config 顶层配置结构
type Config struct {
	Version  int            `json:"version"`
	Runtime  RuntimeConfig  `json:"runtime"`
	Server   ServerConfig   `json:"server"`
	Network  NetworkConfig  `json:"network"`
	LLM      LLMConfig      `json:"llm"`
	Telegram TelegramConfig `json:"telegram"`
	Log      LogConfig      `json:"log"`
}

// RuntimeConfig 运行时行为配置
type RuntimeConfig struct {
	// Mode 运行模式: cli | server | telegram | both
	Mode string `json:"mode"`
	// WorkDir 工作目录，用于覆盖 os.Getwd()（服务化后 cwd 不可靠）
	WorkDir string `json:"workDir"`
	// Instance 实例名，支持同机多实例（默认 default）
	Instance string `json:"instance"`
}

// ServerConfig WebSocket 服务配置
type ServerConfig struct {
	Enabled bool `json:"enabled"`
	// Addr 监听地址，默认仅本机 127.0.0.1:9090（安全）
	Addr string `json:"addr"`
}

// NetworkConfig 网络代理配置
type NetworkConfig struct {
	// Proxy HTTP/HTTPS 代理地址（如 http://127.0.0.1:1088）。
	// 为空表示不使用代理；服务化后需显式配置（进程不继承 shell 环境变量）。
	Proxy string `json:"proxy"`
}

// LLMConfig 大模型接入配置
type LLMConfig struct {
	Provider string `json:"provider"` // openai/deepseek/moonshot/zhipu/qwen/ollama/custom
	APIKey   string `json:"apiKey"`   // 明文存储（后续可替换为系统密钥库）
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
}

// TelegramConfig Telegram Bot 配置
type TelegramConfig struct {
	// Enabled 服务启动时是否自动拉起 Telegram Bot
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level      string `json:"level"`      // debug | info | warn | error
	File       string `json:"file"`       // 日志文件路径，空表示仅输出到 stderr
	MaxSizeMB  int    `json:"maxSizeMB"`  // 单个日志文件上限（MB）
	MaxBackups int    `json:"maxBackups"` // 保留的历史日志文件数
}

// DefaultDir 返回用户级配置目录 ~/.lite-agent
func DefaultDir(homeDir string) string {
	return filepath.Join(homeDir, DirName)
}

// DefaultPath 返回用户级配置文件路径 ~/.lite-agent/config.json
func DefaultPath(homeDir string) string {
	return filepath.Join(DefaultDir(homeDir), FileName)
}

// LogDir 返回默认日志目录 ~/.lite-agent/logs
func LogDir(homeDir string) string {
	return filepath.Join(DefaultDir(homeDir), "logs")
}

// Default 返回内置默认配置（homeDir 用于推导日志路径）
func Default(homeDir string) *Config {
	return &Config{
		Version: CurrentVersion,
		Runtime: RuntimeConfig{
			Mode:     ModeCLI,
			WorkDir:  "",
			Instance: "default",
		},
		Server: ServerConfig{
			Enabled: false,
			Addr:    "127.0.0.1:9090",
		},
		LLM: LLMConfig{},
		Telegram: TelegramConfig{
			Enabled: false,
		},
		Log: LogConfig{
			Level:      "info",
			File:       filepath.Join(LogDir(homeDir), "lite-agent.log"),
			MaxSizeMB:  10,
			MaxBackups: 7,
		},
	}
}

// Load 从 path 加载配置。文件不存在时返回 Default(homeDir)（不报错）。
// 解析失败时返回错误，由调用方决定是否降级。
func Load(path, homeDir string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(homeDir), nil
		}
		return nil, fmt.Errorf("读取配置失败 %s: %w", path, err)
	}

	cfg := Default(homeDir)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败 %s: %w", path, err)
	}

	// 版本兜底
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	// 关键默认值兜底（兼容手工编辑缺失字段）
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:9090"
	}
	if cfg.Runtime.Instance == "" {
		cfg.Runtime.Instance = "default"
	}
	if cfg.Log.File == "" {
		cfg.Log.File = filepath.Join(LogDir(homeDir), "lite-agent.log")
	}
	if cfg.Log.MaxSizeMB <= 0 {
		cfg.Log.MaxSizeMB = 10
	}
	if cfg.Log.MaxBackups <= 0 {
		cfg.Log.MaxBackups = 7
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}

	return cfg, nil
}

// Save 将配置原子写入 path：确保目录 0700、文件 0600。
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建配置目录失败 %s: %w", dir, err)
	}
	// 目录权限收紧（已存在时 MkdirAll 不会改权限）
	_ = os.Chmod(dir, 0700)

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("写入临时配置失败 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置失败 %s: %w", path, err)
	}
	// 确保最终文件权限
	_ = os.Chmod(path, 0600)
	return nil
}

// Store 线程安全的配置存储，供多协程（如 server 连接处理）读写。
type Store struct {
	mu      sync.RWMutex
	path    string
	homeDir string
	cfg     *Config
}

// Open 打开（或初始化）配置文件并返回 Store。
// path 为空时使用 DefaultPath(homeDir)。
func Open(path, homeDir string) (*Store, error) {
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("获取用户主目录失败: %w", err)
		}
		homeDir = h
	}
	if path == "" {
		path = DefaultPath(homeDir)
	}

	cfg, err := Load(path, homeDir)
	if err != nil {
		return nil, err
	}

	return &Store{
		path:    path,
		homeDir: homeDir,
		cfg:     cfg,
	}, nil
}

// Path 返回当前配置文件路径
func (s *Store) Path() string { return s.path }

// HomeDir 返回推导出的用户主目录
func (s *Store) HomeDir() string { return s.homeDir }

// Get 返回配置的副本（调用方持有独立快照，避免并发读写）
func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := *s.cfg
	return &cp
}

// Update 在写锁保护下修改配置并原子落盘。修改函数返回错误时不写盘。
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fn != nil {
		if err := fn(s.cfg); err != nil {
			return err
		}
	}
	return s.cfg.Save(s.path)
}

// Save 将当前内存配置落盘
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Save(s.path)
}
