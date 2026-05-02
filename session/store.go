package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store 会话文件存储
type Store struct {
	baseDir string
	mu      sync.Mutex // 保护并发文件写入
}

// NewStore 创建 Store，自动创建存储目录
func NewStore(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}
	return &Store{baseDir: baseDir}, nil
}

// Save 保存会话到 JSON 文件（原子写入：tmp + rename）
func (s *Store) Save(session *Session) error {
	if session.MessageCount == 0 {
		return nil // 空会话不保存
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.filePath(session.ID)
	tmpPath := filePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath) // 清理临时文件
		return fmt.Errorf("替换会话文件失败: %w", err)
	}

	return nil
}

// Load 按 ID 加载完整会话
func (s *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(s.filePath(id))
	if err != nil {
		return nil, fmt.Errorf("读取会话文件失败: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("解析会话文件失败: %w", err)
	}

	return &session, nil
}

// List 列出所有会话元数据，按时间倒序
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	var metas []Meta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		session, err := s.Load(id)
		if err != nil {
			continue // 跳过损坏的文件
		}

		metas = append(metas, session.Meta())
	}

	// 按 ID 倒序排列（ID 本身包含时间，天然有序）
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].ID > metas[j].ID
	})

	return metas, nil
}

// Latest 加载最近一次会话
func (s *Store) Latest() (*Session, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, nil // 无历史会话
	}
	return s.Load(metas[0].ID)
}

// Delete 删除会话文件
func (s *Store) Delete(id string) error {
	filePath := s.filePath(id)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("会话 %s 不存在", id)
	}
	return os.Remove(filePath)
}

// filePath 返回会话文件完整路径
func (s *Store) filePath(id string) string {
	return filepath.Join(s.baseDir, id+".json")
}
