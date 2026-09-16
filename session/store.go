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
	mu      sync.RWMutex // 保护并发读写：写独占，读共享
}

// NewStore 创建 Store，自动创建存储目录
func NewStore(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}
	return &Store{baseDir: baseDir}, nil
}

// validSessionID 校验 session ID 是否合法。
// 仅允许字母、数字、'-' 和 '_'，防止路径穿越（如 "../xxx"）。
func validSessionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// Save 保存会话到 JSON 文件（原子写入：tmp + rename）
func (s *Store) Save(session *Session) error {
	if !validSessionID(session.ID) {
		return fmt.Errorf("非法会话 ID: %q", session.ID)
	}
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

	// Windows 兼容：如果目标已存在，需先删除才能 Rename
	os.Remove(filePath) //nolint:errcheck
	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath) // 清理临时文件
		return fmt.Errorf("替换会话文件失败: %w", err)
	}

	return nil
}

// Load 按 ID 加载完整会话
func (s *Store) Load(id string) (*Session, error) {
	if !validSessionID(id) {
		return nil, fmt.Errorf("非法会话 ID: %q", id)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

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

// sessionMetaJSON 仅用于解析会话文件中的元数据字段。
// 与 Session 共用 JSON 标签，但刻意不含 Messages，避免反序列化整个消息数组。
type sessionMetaJSON struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
}

// loadMeta 只解析单个会话文件的元数据（调用方需自行持有读锁）
func (s *Store) loadMeta(id string) (Meta, error) {
	data, err := os.ReadFile(s.filePath(id))
	if err != nil {
		return Meta{}, err
	}

	var m sessionMetaJSON
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, err
	}

	return Meta{
		ID:           m.ID,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		Preview:      m.Preview,
		MessageCount: m.MessageCount,
	}, nil
}

// List 列出所有会话元数据，按时间倒序。
// 仅读取每个会话文件的元数据字段，不解析 Messages，避免大文件开销。
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var metas []Meta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		meta, err := s.loadMeta(id)
		if err != nil {
			continue // 跳过损坏的文件
		}
		if meta.ID == "" {
			meta.ID = id // 兼容极旧文件缺少 id 字段的情况
		}

		metas = append(metas, meta)
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
	if !validSessionID(id) {
		return fmt.Errorf("非法会话 ID: %q", id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
