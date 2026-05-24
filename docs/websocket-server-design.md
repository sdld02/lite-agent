# WebSocket 服务模式 — 变更设计文档

> 目标：为 lite-agent 新增一种"常驻服务"启动方式，通过 WebSocket 与外部程序通信，接受命令并执行。

---

## 一、现状分析

### 1.1 当前架构

```
main.go (入口，交互式 CLI)
  ├── agent/agent.go        → Agent 核心（Run / RunStream）
  ├── llm/openai.go          → OpenAI 兼容 LLM 提供者（Chat / ChatStream / ChatStreamReasoning）
  ├── tools/                 → 所有工具实现
  │   ├── builtin.go         → CalculatorTool, SystemInfoTool
  │   ├── shell.go           → ShellTool
  │   ├── file_tools.go      → FileEdit/Write/Diff/Read
  │   ├── code_tools.go      → CodeProbe/Stats
  │   ├── lsp_tools.go       → LSP 工具
  │   ├── task_tools.go      → Task 管理工具
  │   └── agent_tools.go     → 子 Agent 工具
  └── session/
      ├── session.go         → Session 数据结构
      └── store.go           → JSON 文件持久化存储
```

### 1.2 当前启动方式

仅支持 **交互式 CLI**：

```bash
go run main.go -provider=deepseek -key=xxx
# 进入 readline 循环，读取 stdin 逐行处理
```

核心循环（`main.go` 第 ~340 行）：

```go
reader := bufio.NewReader(os.Stdin)
for {
    fmt.Print("👤 You: ")
    input, _ := reader.ReadString('\n')
    // 命令路由: quit/exit/prompt/sessions/new/load/delete
    // 运行 Agent: ag.Run(ctx, input) 或 ag.RunStream(ctx, input, ...)
}
```

### 1.3 关键接口

| 接口 | 方法 | 用途 |
|------|------|------|
| `agent.Tool` | `Name() / Description() / Parameters() / Execute()` | 所有工具统一接口 |
| `agent.LLMProvider` | `Chat(ctx, messages, tools) -> *Message` | 非流式 LLM |
| `agent.StreamProvider` | `ChatStream(ctx, ... , callback) -> *Message` | 流式 LLM |
| `agent.ReasoningStreamProvider` | `ChatStreamReasoning(ctx, ..., onContent, onReasoning)` | 推理+流式 |

### 1.4 Agent 运行流程

```
用户输入
  → Agent.Run / Agent.RunStream
    → 构建消息列表（system + history + user）
    → 循环（最大 maxSteps 步）：
      → 调用 LLM（Chat 或 ChatStream）
      → 如果 LLM 返回 tool_calls → 执行工具 → 结果追加到消息列表 → 继续循环
      → 如果 LLM 无 tool_calls → 返回最终文本
```

---

## 二、目标架构

### 2.1 新增 `-server` 启动模式

```bash
# 以 WebSocket 服务方式启动
go run main.go -server -addr=:9090 -provider=deepseek -key=xxx
```

启动后：
- 启动 HTTP 服务器，监听指定端口
- `/ws` 路径用于 WebSocket 升级
- 支持多个客户端同时连接，每个连接有独立的 Agent 实例和会话
- 进程常驻后台，直到收到 SIGINT/SIGTERM

### 2.2 整体架构图

```
┌──────────────┐       WebSocket        ┌──────────────────────────────┐
│  外部程序 A   │◄──────────────────────►│                              │
│  (VS Code)   │   JSON 消息协议         │   lite-agent server          │
└──────────────┘                         │                              │
                                         │  ┌────────────────────────┐  │
┌──────────────┐       WebSocket        │  │  Connection Handler A   │  │
│  外部程序 B   │◄──────────────────────►│  │  ┌──────────────────┐  │  │
│  (Web UI)    │   JSON 消息协议         │  │  │  Agent 实例       │  │  │
└──────────────┘                         │  │  │  Session          │  │  │
                                         │  │  └──────────────────┘  │  │
                                         │  └────────────────────────┘  │
                                         │                              │
                                         │  ┌────────────────────────┐  │
                                         │  │  Connection Handler B   │  │
                                         │  │  ┌──────────────────┐  │  │
                                         │  │  │  Agent 实例       │  │  │
                                         │  │  │  Session          │  │  │
                                         │  │  └──────────────────┘  │  │
                                         │  └────────────────────────┘  │
                                         └──────────────────────────────┘
```

---

## 三、新增文件清单

### 3.1 `server/protocol.go` — WebSocket 消息协议定义

定义所有消息类型，采用 JSON 格式，按 `type` 字段区分：

```go
package server

// ========== 客户端 → 服务端 ==========

// ClientMessage 客户端发来的消息
type ClientMessage struct {
    Type      string `json:"type"`                // 消息类型
    Content   string `json:"content,omitempty"`   // chat 类型的用户输入
    SessionID string `json:"session_id,omitempty"` // 目标会话 ID
}

// 支持的客户端消息类型常量
const (
    MsgTypeChat          = "chat"           // 发送对话消息
    MsgTypeNewSession    = "new_session"    // 创建新会话
    MsgTypeLoadSession   = "load_session"   // 加载历史会话
    MsgTypeListSessions  = "list_sessions"  // 列出所有会话
    MsgTypeDeleteSession = "delete_session" // 删除会话
    MsgTypeGetStatus     = "get_status"     // 获取服务状态
)

// ========== 服务端 → 客户端 ==========

// ServerMessage 服务端发出的消息
type ServerMessage struct {
    Type      string       `json:"type"`
    Content   string       `json:"content,omitempty"`   // 文本内容
    ToolCall  *ToolCallMsg `json:"tool_call,omitempty"` // 工具调用信息
    Result    string       `json:"result,omitempty"`    // 工具执行结果
    Error     string       `json:"error,omitempty"`     // 错误信息
    Response  string       `json:"response,omitempty"`  // 完整响应（done 时）
    Session   *SessionInfo `json:"session,omitempty"`   // 会话信息
    Sessions  []SessionInfo `json:"sessions,omitempty"` // 会话列表
    Status    *StatusInfo  `json:"status,omitempty"`    // 服务状态
}

// 支持的服务端消息类型常量
const (
    MsgTypeConnected   = "connected"    // 连接建立成功
    MsgTypeContent     = "content"      // 流式正文片段
    MsgTypeReasoning   = "reasoning"    // 流式推理片段
    MsgTypeToolCall    = "tool_call"    // 工具调用开始
    MsgTypeToolResult  = "tool_result"  // 工具调用结果
    MsgTypeDone        = "done"         // 本轮对话完成
    MsgTypeError       = "error"        // 错误
    MsgTypeSessionInfo = "session_info" // 当前会话信息
    MsgTypeSessionList = "session_list" // 会话列表
    MsgTypeStatus      = "status"       // 服务状态
)

// ToolCallMsg 工具调用消息
type ToolCallMsg struct {
    Name string                 `json:"name"`
    Args map[string]interface{} `json:"args"`
}

// SessionInfo 会话摘要（发送给客户端）
type SessionInfo struct {
    ID           string `json:"id"`
    CreatedAt    string `json:"created_at"`
    UpdatedAt    string `json:"updated_at"`
    MessageCount int    `json:"message_count"`
    Preview      string `json:"preview"`
}

// StatusInfo 服务状态
type StatusInfo struct {
    ActiveConnections int    `json:"active_connections"`
    Uptime            string `json:"uptime"`
    Version           string `json:"version"`
}
```

### 3.2 `server/handler.go` — 单连接处理器

每个 WebSocket 连接由一个 `ConnectionHandler` 管理，拥有独立的 Agent 实例。

```go
package server

// ConnectionHandler 管理单个 WebSocket 连接
type ConnectionHandler struct {
    conn      *websocket.Conn
    agent     *agent.Agent
    store     *session.Store
    session   *session.Session
    registry  *agentpkg.ToolRegistry
    provider  agent.LLMProvider
    writeMu   sync.Mutex       // 写锁，防止并发写入 WebSocket
    ctx       context.Context
    cancel    context.CancelFunc
}

// 职责：
// 1. 读取客户端消息循环（readLoop）
// 2. 消息路由分发（handleMessage）
// 3. 调用 Agent.RunStream，将回调转为 WebSocket 消息推送
// 4. 会话的创建/加载/列表/删除操作
// 5. 连接断开时自动保存会话
```

#### 消息路由：

| 客户端消息类型 | 处理方式 |
|--------------|---------|
| `chat` | 调用 `agent.RunStream()`，通过回调将内容实时推送给客户端 |
| `new_session` | 保存当前会话，创建新 Session，清空 Agent memory |
| `load_session` | 保存当前会话，加载目标 Session，恢复 Agent memory |
| `list_sessions` | 调用 `store.List()`，返回会话列表 |
| `delete_session` | 调用 `store.Delete()`，返回结果 |
| `get_status` | 返回服务运行状态 |

#### 流式输出转 WebSocket 推送：

```go
// 在 handleChat 中：
response, err := h.agent.RunStream(ctx, input,
    // onChunk: 正文 → JSON → WebSocket
    func(chunk string) {
        h.sendMessage(ServerMessage{Type: MsgTypeContent, Content: chunk})
    },
    // onReasoning: 推理 → JSON → WebSocket
    func(chunk string) {
        h.sendMessage(ServerMessage{Type: MsgTypeReasoning, Content: chunk})
    },
    // onFlush: tool_call 执行前发送累积内容（可选择渲染处理）
    func(content string) {
        // WebSocket 模式下不需要清屏操作，可以发送一个 flush 标记
    },
)
// 发送最终结果
h.sendMessage(ServerMessage{Type: MsgTypeDone, Response: response})
```

### 3.3 `server/server.go` — WebSocket 服务主控

```go
package server

// Server WebSocket 服务
type Server struct {
    addr       string
    store      *session.Store
    registry   *agentpkg.ToolRegistry
    provider   agent.LLMProvider
    systemPrompt string
    maxSteps   int
    startTime  time.Time

    // 连接管理
    connMu     sync.RWMutex
    conns      map[*websocket.Conn]*ConnectionHandler
    connCount  int
}

// NewServer 创建服务实例
func NewServer(addr string, store *session.Store, registry *agentpkg.ToolRegistry,
    provider agent.LLMProvider, systemPrompt string, maxSteps int) *Server

// Start 启动 HTTP 服务，监听并处理 WebSocket 升级
func (s *Server) Start() error

// Shutdown 优雅关闭：关闭所有连接 → 保存所有会话 → 停止 HTTP 服务
func (s *Server) Shutdown(ctx context.Context) error

// handleUpgrade 处理 /ws 路径的 WebSocket 升级请求
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request)
```

#### 依赖项

需要引入一个 WebSocket 库。推荐 **`github.com/gorilla/websocket`**，这是 Go 生态中最成熟的 WebSocket 实现。

```bash
go get github.com/gorilla/websocket
```

---

## 四、修改文件清单

### 4.1 `main.go` — 添加 `-server` 模式

#### 4.1.1 新增命令行参数

```go
serverMode := flag.Bool("server", false, "以 WebSocket 服务模式启动（常驻后台）")
serverAddr := flag.String("addr", ":9090", "WebSocket 服务监听地址")
```

#### 4.1.2 修改 main() 函数流程

```go
func main() {
    // ... 现有参数解析保持不变 ...

    // 初始化 Agent、工具、SessionStore（提取为公共初始化函数）

    if *serverMode {
        runServerMode(finalBaseURL, finalModel, ...)
    } else {
        runInteractiveMode(finalBaseURL, finalModel, ...)
    }
}
```

#### 4.1.3 提取公共初始化逻辑

将当前 `main()` 中的 Agent 初始化、工具注册、SessionStore 创建等逻辑提取为独立函数：

```go
// initAgent 初始化 Agent、工具注册表、会话存储
// 返回初始化好的组件，供 CLI 和 Server 模式共用
func initAgent(homeDir string, finalBaseURL, finalModel, finalAPIKey string) (
    *agent.Agent, *agentpkg.ToolRegistry, *session.Store, string)
```

#### 4.1.4 `runServerMode()` 函数

```go
func runServerMode(ctx context.Context, homeDir, finalBaseURL, finalModel, finalAPIKey string) {
    ag, registry, store, systemPrompt := initAgent(homeDir, finalBaseURL, finalModel, finalAPIKey)

    // 获取 LLM Provider 引用
    provider := llm.NewOpenAIProvider(llm.OpenAIConfig{...})

    // 创建 WebSocket 服务
    srv := server.NewServer(*serverAddr, store, registry, provider, systemPrompt, 50)

    // 注册信号处理（SIGINT/SIGTERM → 优雅关闭）
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        fmt.Println("\n🛑 收到退出信号，正在关闭服务...")
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := srv.Shutdown(shutdownCtx); err != nil {
            fmt.Printf("关闭服务出错: %v\n", err)
        }
    }()

    // 启动服务（阻塞）
    fmt.Printf("🚀 WebSocket 服务已启动: ws://%s/ws\n", *serverAddr)
    if err := srv.Start(); err != nil && err != http.ErrServerClosed {
        fmt.Printf("服务启动失败: %v\n", err)
        os.Exit(1)
    }
}
```

### 4.2 `agent/agent.go` — 可选微调

#### 4.2.1 提取工具调用的打印逻辑

当前 `executeToolCalls()` 方法中包含 `fmt.Printf()` 调用（第 ~200 行）：

```go
fmt.Printf("\n🔧 调用工具: %s\n", tc.Function.Name)
// ...
fmt.Printf("   结果: %s\n\n", result)
```

在 Server 模式下，这些信息应该通过 WebSocket 发送而不是打印到 stdout。建议：

**方案 A（推荐）**：通过回调通知工具调用事件

```go
// ToolCallObserver 工具调用观察者（可选接口）
type ToolCallObserver func(toolName string, args map[string]interface{}, result string)

// Agent 新增字段
type Agent struct {
    // ... 现有字段 ...
    toolObserver ToolCallObserver  // 可选：工具调用观察者
}

// SetToolObserver 设置工具调用观察者
func (a *Agent) SetToolObserver(observer ToolCallObserver) {
    a.toolObserver = observer
}
```

在 `executeToolCalls()` 中：

```go
// 执行工具
result, err := tool.Execute(ctx, args)
if err != nil {
    result = fmt.Sprintf("错误: %v", err)
}

// 通知观察者（如果已设置）
if a.toolObserver != nil {
    a.toolObserver(tc.Function.Name, args, result)
} else {
    // CLI 模式保持原样
    fmt.Printf("\n🔧 调用工具: %s\n", tc.Function.Name)
    fmt.Printf("   结果: %s\n\n", result)
}
```

在 Server 的 ConnectionHandler 中设置观察者：

```go
h.agent.SetToolObserver(func(name string, args map[string]interface{}, result string) {
    h.sendMessage(ServerMessage{
        Type:     MsgTypeToolCall,
        ToolCall: &ToolCallMsg{Name: name, Args: args},
    })
    h.sendMessage(ServerMessage{
        Type:   MsgTypeToolResult,
        Result: result,
    })
})
```

> 这样可以保持 Agent 核心逻辑干净，CLI 和 Server 各自决定如何呈现工具调用信息。

---

## 五、WebSocket 通信协议详解

### 5.1 连接建立

```
客户端                                   服务端
  │                                        │
  │  ws://host:9090/ws (Upgrade)           │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"connected",                   │
  │   "session":{"id":"20260424-153000",    │
  │              "message_count":3, ...}}   │
  │◄───────────────────────────────────────│
  │                                        │
```

### 5.2 对话流程

```
客户端                                   服务端
  │                                        │
  │  {"type":"chat",                       │
  │   "content":"帮我分析项目结构"}          │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"reasoning","content":"..."}  │  ← 推理片段（可选）
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"content","content":"好的"}    │  ← 正文片段（流式）
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"tool_call",                   │  ← 工具调用通知
  │   "tool_call":{"name":"code_probe",     │
  │                "args":{...}}}           │
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"tool_result",                 │  ← 工具执行结果
  │   "result":"..."}                      │
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"content","content":"..."}    │  ← 继续流式输出
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"done",                        │  ← 本轮完成
  │   "response":"完整响应文本",             │
  │   "session":{...}}                     │
  │◄───────────────────────────────────────│
  │                                        │
```

### 5.3 会话管理流程

```
客户端                                   服务端
  │                                        │
  │  {"type":"list_sessions"}              │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"session_list",               │
  │   "sessions":[{id, preview, ...}, ...]}│
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"load_session",               │
  │   "session_id":"20260424-150000"}     │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"session_info",               │
  │   "session":{"id":"20260424-150000",   │
  │              "message_count":5, ...}}  │
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"new_session"}                │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"session_info",               │
  │   "session":{"id":"20260424-153500",   │
  │              "message_count":0, ...}}  │
  │◄───────────────────────────────────────│
  │                                        │
  │  {"type":"delete_session",             │
  │   "session_id":"20260424-140000"}     │
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"session_info",               │
  │   "result":"已删除"}                    │
  │◄───────────────────────────────────────│
```

### 5.4 错误处理

```
客户端                                   服务端
  │                                        │
  │  {"type":"chat","content":""}          │  ← 空消息
  │───────────────────────────────────────►│
  │                                        │
  │  {"type":"error",                       │
  │   "error":"消息内容不能为空"}            │
  │◄───────────────────────────────────────│
```

---

## 六、文件变更汇总

| 操作 | 文件路径 | 说明 |
|------|---------|------|
| **新增** | `server/server.go` | WebSocket 服务主控（HTTP 服务器、连接管理、优雅关闭） |
| **新增** | `server/protocol.go` | 消息类型定义（客户端/服务端消息结构、常量） |
| **新增** | `server/handler.go` | 单连接处理器（消息路由、Agent 调用、WebSocket 读写） |
| **修改** | `main.go` | 添加 `-server` / `-addr` 参数；提取 `initAgent()` 公共函数；新增 `runServerMode()` |
| **修改** | `agent/agent.go` | 新增 `ToolCallObserver` 回调类型和 `SetToolObserver()` 方法；修改 `executeToolCalls()` 支持通过回调通知 |
| **修改** | `go.mod` / `go.sum` | 新增依赖：`github.com/gorilla/websocket` |

---

## 七、实现步骤（建议顺序）

### Step 1: 添加依赖

```bash
go get github.com/gorilla/websocket
```

### Step 2: 创建 `server/protocol.go`

定义所有消息类型和常量，这是协议基础，其他文件都依赖它。

### Step 3: 修改 `agent/agent.go`

添加 `ToolCallObserver` 支持，改动最小（约 15 行），向后兼容。

### Step 4: 创建 `server/handler.go`

实现 `ConnectionHandler`：
- `readLoop()` — 读取 WebSocket 消息
- `handleMessage()` — 路由分发
- `handleChat()` — 调用 Agent 并推送流式结果
- `sendMessage()` — 线程安全的 WebSocket 写入

### Step 5: 创建 `server/server.go`

实现 `Server`：
- `Start()` — 启动 HTTP 服务
- `handleUpgrade()` — WebSocket 升级处理
- `Shutdown()` — 优雅关闭

### Step 6: 修改 `main.go`

- 提取 `initAgent()` 公共初始化函数
- 新增 `runServerMode()` 函数
- 在 `main()` 中分支：`if *serverMode { runServerMode() } else { runInteractiveMode() }`

### Step 7: 集成测试

```bash
# 启动服务
go run main.go -server -addr=:9090 -provider=deepseek -key=xxx

# 使用 wscat 或 websocat 测试
websocat ws://localhost:9090/ws
# 输入: {"type":"chat","content":"你好"}
# 观察流式返回
```

---

## 八、安全与稳定性考量

| 关注点 | 措施 |
|--------|------|
| **并发安全** | `sendMessage()` 使用 `sync.Mutex` 保护 WebSocket 写入 |
| **连接数限制** | 可选的 `maxConnections` 配置，超过限制返回 503 |
| **消息大小限制** | WebSocket 读取限制（默认 512KB） |
| **超时控制** | WebSocket 读写超时、Agent 执行超时通过 context 传递 |
| **优雅关闭** | 收到 SIGTERM 后：停止接受新连接 → 等待现有请求完成 → 保存所有会话 → 关闭连接 → 退出 |
| **资源清理** | 连接断开时自动保存会话，通过 `defer` 确保 Agent 上下文释放 |
| **错误隔离** | 单个连接的 panic 不影响其他连接和服务主进程 |

---

## 九、扩展可能性

以下为后续可选的增强，不在本次变更范围内，但预留接口：

1. **认证机制**：WebSocket 握手时验证 Token（通过 query string 或 header）
2. **多 Agent 类型路由**：不同 URL 路径对应不同 Agent 配置
3. **健康检查端点**：`GET /health` 返回 JSON 状态
4. **Prometheus 指标**：连接数、请求延迟、工具调用次数
5. **配置热重载**：监听配置文件变更，动态调整 Agent 行为
6. **消息持久化**：可选将 WebSocket 消息写入消息队列（如 NATS）用于审计
