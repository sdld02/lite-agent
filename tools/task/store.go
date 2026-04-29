package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// TaskStore 任务持久化存储接口。
// 支持后续扩展为其他存储后端（如 SQLite、etcd 等）。
type TaskStore interface {
	Create(taskListID string, t Task) (string, error)
	Get(taskListID, taskID string) (*Task, error)
	Update(taskListID, taskID string, updates map[string]interface{}) (*Task, error)
	Delete(taskListID, taskID string) error
	List(taskListID string) ([]Task, error)
}

// FileTaskStore 基于文件系统的任务存储实现。
//
// 存储结构：
//
//	~/.lite-agent/tasks/{taskListId}/
//	  ├── .lock           # 文件锁（syscall.Flock）
//	  ├── .highwatermark  # 最大已分配 ID
//	  ├── 1.json
//	  └── 2.json
type FileTaskStore struct {
	mu       sync.Mutex // 进程内互斥
	basePath string
}

// fileLock 封装 syscall.Flock 文件锁
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

// NewFileTaskStore 创建文件系统任务存储
func NewFileTaskStore(basePath string) *FileTaskStore {
	return &FileTaskStore{basePath: basePath}
}

// BasePath 返回存储根目录
func (s *FileTaskStore) BasePath() string {
	return s.basePath
}

// dir 返回指定任务列表的目录
func (s *FileTaskStore) dir(taskListID string) string {
	return filepath.Join(s.basePath, sanitizePathComponent(taskListID))
}

// lockPath 返回文件锁路径
func (s *FileTaskStore) lockPath(taskListID string) string {
	return filepath.Join(s.dir(taskListID), ".lock")
}

// highWaterMarkPath 返回 high water mark 文件路径
func (s *FileTaskStore) highWaterMarkPath(taskListID string) string {
	return filepath.Join(s.dir(taskListID), ".highwatermark")
}

// taskPath 返回任务 JSON 文件路径
func (s *FileTaskStore) taskPath(taskListID, taskID string) string {
	return filepath.Join(s.dir(taskListID), sanitizePathComponent(taskID)+".json")
}

// Create 创建新任务，返回分配的任务 ID。
// 使用文件锁保证并发安全，high water mark 防止 ID 复用。
func (s *FileTaskStore) Create(taskListID string, t Task) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.dir(taskListID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create task dir: %w", err)
	}

	lock, err := newFileLock(s.lockPath(taskListID))
	if err != nil {
		return "", fmt.Errorf("create lock: %w", err)
	}
	if err := lock.Lock(); err != nil {
		return "", fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck

	// 读取 high water mark，生成新 ID
	hwm := s.readHighWaterMark(taskListID)
	// 同时扫描已有文件获取最大 ID（防御 high water mark 丢失）
	maxFromFiles := s.findMaxIDFromFiles(taskListID)
	if maxFromFiles > hwm {
		hwm = maxFromFiles
	}

	newID := strconv.Itoa(hwm + 1)
	t.ID = newID

	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal task: %w", err)
	}

	if err := os.WriteFile(s.taskPath(taskListID, newID), data, 0644); err != nil {
		return "", fmt.Errorf("write task file: %w", err)
	}

	s.writeHighWaterMark(taskListID, hwm+1)
	return newID, nil
}

// Get 读取单个任务
func (s *FileTaskStore) Get(taskListID, taskID string) (*Task, error) {
	data, err := os.ReadFile(s.taskPath(taskListID, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task file: %w", err)
	}

	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}
	return &t, nil
}

// Update 更新任务字段。updates 为要更新的字段 map，key 为字段名。
// 支持：subject, description, activeForm, status, owner, blocks, blockedBy, metadata
func (s *FileTaskStore) Update(taskListID, taskID string, updates map[string]interface{}) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock, err := newFileLock(s.taskPath(taskListID, taskID))
	if err != nil {
		return nil, fmt.Errorf("create task lock: %w", err)
	}
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("acquire task lock: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck

	existing, err := s.Get(taskListID, taskID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, nil
	}

	// 应用更新
	if v, ok := updates["subject"]; ok {
		existing.Subject = v.(string)
	}
	if v, ok := updates["description"]; ok {
		existing.Description = v.(string)
	}
	if v, ok := updates["activeForm"]; ok {
		existing.ActiveForm = v.(string)
	}
	if v, ok := updates["status"]; ok {
		existing.Status = TaskStatus(v.(string))
	}
	if v, ok := updates["owner"]; ok {
		if v == nil {
			existing.Owner = ""
		} else {
			existing.Owner = v.(string)
		}
	}
	if v, ok := updates["blocks"]; ok {
		existing.Blocks = toStringSlice(v)
	}
	if v, ok := updates["blockedBy"]; ok {
		existing.BlockedBy = toStringSlice(v)
	}
	if v, ok := updates["metadata"]; ok {
		existing.Metadata = mergeMetadata(existing.Metadata, v)
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal task: %w", err)
	}

	if err := os.WriteFile(s.taskPath(taskListID, taskID), data, 0644); err != nil {
		return nil, fmt.Errorf("write task file: %w", err)
	}

	return s.Get(taskListID, taskID)
}

// Delete 删除任务，并更新 high water mark。
// 同时清理其他任务中对本任务的引用（blocks/blockedBy）。
func (s *FileTaskStore) Delete(taskListID, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock, err := newFileLock(s.lockPath(taskListID))
	if err != nil {
		return fmt.Errorf("create lock: %w", err)
	}
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lock.Unlock() //nolint:errcheck

	// 更新 high water mark
	if id, err := strconv.Atoi(taskID); err == nil {
		hwm := s.readHighWaterMark(taskListID)
		if id > hwm {
			s.writeHighWaterMark(taskListID, id)
		}
	}

	// 删除任务文件
	path := s.taskPath(taskListID, taskID)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove task file: %w", err)
	}

	// 清理其他任务中对该任务的引用
	tasks, _ := s.listUnlocked(taskListID)
	for _, t := range tasks {
		needsUpdate := false

		newBlocks := removeFromSlice(t.Blocks, taskID)
		if len(newBlocks) != len(t.Blocks) {
			t.Blocks = newBlocks
			needsUpdate = true
		}

		newBlockedBy := removeFromSlice(t.BlockedBy, taskID)
		if len(newBlockedBy) != len(t.BlockedBy) {
			t.BlockedBy = newBlockedBy
			needsUpdate = true
		}

		if needsUpdate {
			data, _ := json.MarshalIndent(t, "", "  ")
			os.WriteFile(s.taskPath(taskListID, t.ID), data, 0644) //nolint:errcheck
		}
	}

	return nil
}

// List 列出指定任务列表的所有任务
func (s *FileTaskStore) List(taskListID string) ([]Task, error) {
	return s.listUnlocked(taskListID)
}

// BlockTask 设置任务依赖：fromTaskID 阻塞 toTaskID
func (s *FileTaskStore) BlockTask(taskListID, fromTaskID, toTaskID string) error {
	from, err := s.Get(taskListID, fromTaskID)
	if err != nil || from == nil {
		return fmt.Errorf("source task not found: %s", fromTaskID)
	}
	to, err := s.Get(taskListID, toTaskID)
	if err != nil || to == nil {
		return fmt.Errorf("target task not found: %s", toTaskID)
	}

	if !contains(from.Blocks, toTaskID) {
		from.Blocks = append(from.Blocks, toTaskID)
		s.Update(taskListID, fromTaskID, map[string]interface{}{"blocks": from.Blocks})
	}
	if !contains(to.BlockedBy, fromTaskID) {
		to.BlockedBy = append(to.BlockedBy, fromTaskID)
		s.Update(taskListID, toTaskID, map[string]interface{}{"blockedBy": to.BlockedBy})
	}
	return nil
}

// AgentStatuses 获取所有 Agent 的任务状态
func (s *FileTaskStore) AgentStatuses(taskListID string, agents []AgentInfo) []AgentStatus {
	tasks, _ := s.List(taskListID)
	if tasks == nil {
		tasks = []Task{}
	}
	agentTasks := make(map[string][]string)
	for _, t := range tasks {
		if t.Status != StatusCompleted && t.Owner != "" {
			agentTasks[t.Owner] = append(agentTasks[t.Owner], t.ID)
		}
	}
	var result []AgentStatus
	for _, a := range agents {
		currentTasks := agentTasks[a.Name]
		if currentTasks == nil {
			currentTasks = agentTasks[a.ID]
		}
		status := "idle"
		if len(currentTasks) > 0 {
			status = "busy"
		}
		result = append(result, AgentStatus{
			AgentID:      a.ID,
			Name:         a.Name,
			Status:       status,
			CurrentTasks: currentTasks,
		})
	}
	return result
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

func (s *FileTaskStore) listUnlocked(taskListID string) ([]Task, error) {
	dir := s.dir(taskListID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task dir: %w", err)
	}
	var tasks []Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		taskID := strings.TrimSuffix(e.Name(), ".json")
		t, err := s.Get(taskListID, taskID)
		if err != nil || t == nil {
			continue
		}
		tasks = append(tasks, *t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		ni, _ := strconv.Atoi(tasks[i].ID)
		nj, _ := strconv.Atoi(tasks[j].ID)
		return ni < nj
	})
	return tasks, nil
}

func (s *FileTaskStore) readHighWaterMark(taskListID string) int {
	data, err := os.ReadFile(s.highWaterMarkPath(taskListID))
	if err != nil {
		return 0
	}
	val, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return val
}

func (s *FileTaskStore) writeHighWaterMark(taskListID string, value int) {
	path := s.highWaterMarkPath(taskListID)
	os.MkdirAll(filepath.Dir(path), 0755) //nolint:errcheck
	os.WriteFile(path, []byte(strconv.Itoa(value)), 0644) //nolint:errcheck
}

func (s *FileTaskStore) findMaxIDFromFiles(taskListID string) int {
	dir := s.dir(taskListID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	maxID := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		idStr := strings.TrimSuffix(e.Name(), ".json")
		if id, err := strconv.Atoi(idStr); err == nil && id > maxID {
			maxID = id
		}
	}
	return maxID
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

func sanitizePathComponent(input string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, input)
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func removeFromSlice(slice []string, item string) []string {
	var result []string
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func toStringSlice(v interface{}) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		var result []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func mergeMetadata(existing map[string]interface{}, updates interface{}) map[string]interface{} {
	if existing == nil {
		existing = make(map[string]interface{})
	}
	upd, ok := updates.(map[string]interface{})
	if !ok {
		return existing
	}
	for k, v := range upd {
		if v == nil {
			delete(existing, k)
		} else {
			existing[k] = v
		}
	}
	return existing
}
