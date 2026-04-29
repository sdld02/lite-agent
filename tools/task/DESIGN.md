# Task 系统设计文档

## 一、设计目标

参考 Claude Code 的 TaskCreate/TaskUpdate/TaskList/TaskGet 工具族，为 `lite-agent` 构建一个**轻量、并发安全、支持多 Agent 协作**的任务管理系统。

### 核心原则

1. **零外部依赖** — 仅使用 Go 标准库（`os` + `syscall` + `encoding/json`）
2. **文件系统即数据库** — 每个任务一个 JSON 文件，无需额外数据库
3. **多进程并发安全** — `sync.Mutex`（进程内）+ `syscall.Flock`（进程间）
4. **多 Agent 就绪** — 通过环境变量隔离身份，共享任务列表
5. **防御性设计** — high water mark 防 ID 复用，删除时级联清理引用

---

## 二、架构概览

```
┌──────────────────────────────────────────────────┐
│                    Agent Layer                     │
│  task_create / task_update / task_list / task_get │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│                  Manager (全局单例)                │
│  ┌──────────────┐  ┌────────────────────────────┐ │
│  │  AgentInfo   │  │  GetTaskListID()           │ │
│  │  (身份信息)   │  │  (ENV > team > session)   │ │
│  └──────────────┘  └────────────────────────────┘ │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│                 FileTaskStore                      │
│  ┌──────────┐  ┌────────────┐  ┌───────────────┐ │
│  │  CRUD    │  │  FileLock  │  │  HighWaterMark│ │
│  │  (JSON)  │  │  (Flock)   │  │  (防ID复用)    │ │
│  └──────────┘  └────────────┘  └───────────────┘ │
└────────────────────┬─────────────────────────────┘
                     │
┌────────────────────▼─────────────────────────────┐
│               File System                         │
│  ~/.lite-agent/tasks/{taskListId}/               │
│  ├── .lock            ← Flock 排他锁              │
│  ├── .highwatermark   ← 最大已分配 ID              │
│  ├── 1.json           ← Task #1                  │
│  ├── 2.json           ← Task #2                  │
│  └── ...                                          │
└──────────────────────────────────────────────────┘
```

---

## 三、数据模型

### Task 结构

```go
type Task struct {
    ID          string                 // 自增数字 ID（如 "1", "2", "3"）
    Subject     string                 // 任务标题，祈使句风格
    Description string                 // 任务详细描述
    ActiveForm  string                 // 进行中时的显示文本（可选）
    Owner       string                 // 负责人 Agent 名称
    Status      TaskStatus             // pending | in_progress | completed
    Blocks      []string               // 被此任务阻塞的任务 ID
    BlockedBy   []string               // 阻塞此任务的任务 ID
    Metadata    map[string]interface{} // 附加元数据
}
```

### 状态机

```
                    ┌──────────┐
                    │  pending  │  ← 新建任务
                    └────┬─────┘
                         │ task_update status:"in_progress"
                         │ (自动设置 owner = 当前 Agent)
                    ┌────▼─────┐
                    │in_progress│
                    └────┬─────┘
                         │ task_update status:"completed"
                    ┌────▼─────┐
                    │ completed │
                    └──────────┘

    任意状态 ── status:"deleted" ──→ 删除文件 + 清理引用
```

---

## 四、存储引擎 `store.go`

### 4.1 设计决策

**为什么选文件系统而不是 SQLite/etcd？**

- **零配置**：无需安装数据库，Agent 启动即可用
- **可调试**：任务内容直接 `cat ~/.lite-agent/tasks/{id}/1.json` 查看
- **可扩展**：`TaskStore` 接口允许后续替换为 SQLite/etcd 后端

### 4.2 High Water Mark 机制

```
创建任务流程：
1. 获取列表级 Flock 排他锁
2. 读取 .highwatermark → 当前值为 N
3. 扫描磁盘文件最大 ID 作为防御 → 取 max(hwm, maxFileID)
4. 新任务 ID = max + 1
5. 写入 {newID}.json
6. 更新 .highwatermark = newID
7. 释放锁
```

**防御意义**：即使 `.highwatermark` 文件丢失或被篡改，`findMaxIDFromFiles()` 会从已有文件名恢复正确的 ID 计数器，杜绝 ID 复用。

### 4.3 删除任务的级联清理

```
deleteTask("3"):
  1. 获取列表级 Flock
  2. 更新 high water mark（如果 3 > hwm）
  3. 删除 3.json
  4. 遍历所有其他任务
     - 如果某任务的 blocks 包含 "3" → 移除
     - 如果某任务的 blockedBy 包含 "3" → 移除
  5. 释放锁
```

### 4.4 并发安全

| 层级 | 机制 | 作用域 |
|------|------|--------|
| `sync.Mutex` | 进程内互斥 | 同一个 Go 进程内的 goroutine |
| `syscall.Flock(LOCK_EX)` | 进程间互斥 | 多个 Agent 进程共享同一任务列表 |

`fileLock` 封装：

```go
type fileLock struct { f *os.File }

func newFileLock(path string) (*fileLock, error) {
    // O_CREATE 保证锁文件一定存在
    f, _ := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
    return &fileLock{f: f}, nil
}

func (l *fileLock) Lock() error   { return syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX) }
func (l *fileLock) Unlock() error { defer l.f.Close(); return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN) }
```

---

## 五、多 Agent 支持 `manager.go`

### 5.1 身份识别

通过环境变量注入 Agent 身份：

| 环境变量 | 作用 | 示例 |
|----------|------|------|
| `LITE_AGENT_ID` | Agent 唯一标识 | `agent-001` |
| `LITE_AGENT_NAME` | Agent 显示名称 | `coder-bob` |
| `LITE_AGENT_COLOR` | Agent 颜色标记 | `#ff6b6b` |
| `LITE_TEAM_NAME` | 团队名（共享任务列表） | `my-squad` |
| `LITE_TASK_LIST_ID` | 显式指定任务列表 ID | `project-x` |

### 5.2 TaskListID 解析优先级

```
LITE_TASK_LIST_ID (显式指定)
  → LITE_TEAM_NAME (多 Agent 共享)
    → LITE_SESSION_ID (单 Agent 隔离)
      → "default" (兜底)
```

### 5.3 多 Agent 协作场景

```bash
# Agent A — 创建任务
LITE_AGENT_NAME=alice LITE_TEAM_NAME=dev-team ./lite-agent
# → task_create { "subject": "实现登录 API" }
# → 任务自动分配 owner = "alice"

# Agent B — 查看并认领任务
LITE_AGENT_NAME=bob LITE_TEAM_NAME=dev-team ./lite-agent
# → task_list 看到 alice 的任务
# → task_update { "taskId": "1", "status": "in_progress" }
# → 自动设置 owner = "bob"
```

### 5.4 自动认领逻辑

`TaskUpdateTool` 中当标记 `in_progress` 时：
```
如果 existing.Owner == "" 且 mgr.AgentInfo.Name != ""：
  → 自动设置 owner = mgr.AgentInfo.Name
```

这保证了在 team 模式下，第一个开始工作的 Agent 自动成为任务负责人。

---

## 六、工具 API 设计

### 6.1 TaskCreateTool

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `subject` | string | ✅ | 任务标题 |
| `description` | string | ✅ | 任务详细描述 |
| `activeForm` | string | ❌ | 进行中显示文本 |
| `metadata` | object | ❌ | 附加元数据 |

**输出**：`{ "task": { "id": "1", "subject": "..." }, "owner": "agent-name" }`

### 6.2 TaskUpdateTool

| 参数 | 类型 | 说明 |
|------|------|------|
| `taskId` | string | 要更新的任务 ID |
| `status` | enum | `pending` / `in_progress` / `completed` / `deleted` |
| `subject` | string | 新标题 |
| `description` | string | 新描述 |
| `owner` | string | 新负责人 |
| `addBlocks` | []string | 被此任务阻塞的任务 |
| `addBlockedBy` | []string | 阻塞此任务的任务 |
| `metadata` | object | 合并到 metadata（null 删除 key） |

**特殊行为**：
- `status: "deleted"` → 删除文件 + 级联清理引用
- `status: "in_progress"` + owner 为空 → 自动认领
- `status: "completed"` → 返回提示 "调用 task_list 查找下一个任务"

### 6.3 TaskListTool

无参数。返回所有非 `_internal` 任务，已完成任务的 `blockedBy` 引用会被过滤。

### 6.4 TaskGetTool

| 参数 | 类型 | 说明 |
|------|------|------|
| `taskId` | string | 任务 ID |

返回完整任务详情，包括 `description`、`blocks`、`metadata`。

---

## 七、与 Claude Code 的对比

| 维度 | Claude Code | lite-agent (本实现) |
|------|-------------|---------------------|
| 语言 | TypeScript | Go |
| 存储 | 文件系统 + proper-lockfile | 文件系统 + syscall.Flock |
| 工具注册 | `buildTool()` 声明式工厂 | `agent.Tool` 接口 |
| 钩子系统 | shell 命令 exit code | 未实现（待扩展） |
| 回滚机制 | TaskCreated hook 失败 → deleteTask | 未实现（待扩展） |
| 多 Agent | AsyncLocalStorage + tmux | 环境变量 + 共享文件系统 |
| Agent 邮箱 | writeToMailbox | 未实现（待扩展） |
| 验证提醒 | verificationNudgeNeeded | 未实现（待扩展） |

---

## 八、扩展方向

### 8.1 存储后端可替换

`TaskStore` 是接口，可切换到 SQLite/etcd：

```go
type TaskStore interface {
    Create(taskListID string, t Task) (string, error)
    Get(taskListID, taskID string) (*Task, error)
    Update(taskListID, taskID string, updates map[string]interface{}) (*Task, error)
    Delete(taskListID, taskID string) error
    List(taskListID string) ([]Task, error)
}
```

### 8.2 钩子系统

在 `TaskCreateTool.Execute` 和 `TaskUpdateTool.Execute` 中注入钩子：

```go
// 创建任务前/后钩子
type TaskHook func(ctx context.Context, task *Task) error

// 在 Execute 中调用
for _, hook := range hooks {
    if err := hook(ctx, &task); err != nil {
        store.Delete(taskListID, taskID) // 回滚
        return err
    }
}
```

### 8.3 Agent 邮箱通知

当 `TaskUpdateTool` 分配 owner 时，向被分配者发送通知：

```go
if updates["owner"] != "" {
    writeMailboxNotification(updates["owner"], "你被分配了任务 #"+taskID)
}
```

### 8.4 Claim 认领机制

添加 `task_claim` 工具，原子检查 Agent 是否空闲：

```go
func claimTask(taskListID, taskID, agentID string) error {
    lock.Lock()
    defer lock.Unlock()
    
    task := getTask(taskID)
    if task.Owner != "" { return ErrAlreadyClaimed }
    if task.Status == "completed" { return ErrAlreadyDone }
    if hasActiveBlockers(task) { return ErrBlocked }
    
    task.Owner = agentID
    task.Status = "in_progress"
    save(task)
}
```

### 8.5 验证提醒

当所有任务完成且 ≥3 个任务中无验证步骤时，提示执行验证：

```go
if allDone && len(tasks) >= 3 && !hasVerificationStep(tasks) {
    result["hint"] = "建议创建验证任务确认所有功能正常"
}
```

---

## 九、文件清单

```
tools/task/
├── types.go      # 62 行  数据类型定义
├── store.go      # 367 行  文件系统存储引擎
├── manager.go    # 97 行   全局管理器 + Agent 身份
├── create.go     # 121 行  TaskCreateTool
├── update.go     # 237 行  TaskUpdateTool
├── list.go       # 103 行  TaskListTool
└── get.go        # 94 行   TaskGetTool

tools/task_tools.go  # 30 行  工具注册入口
```

总计约 **1111 行** Go 代码，零外部依赖。
