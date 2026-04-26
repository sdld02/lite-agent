# LSP Tool 设计文档

## 概述

LSP Tool 将 Language Server Protocol（LSP）的代码智能能力集成到 AI Agent 中。AI 模型通过单一工具入口即可获得 IDE 级别的代码理解能力——跳转定义、查找引用、悬停文档、调用层次等。

整个设计参考 Claude Code 的 LSPTool，用 Go 语言完整复刻其核心架构理念。

---

## 核心设计理念

### 1. 单一工具 + 多操作模式

不为一组 LSP 能力创建多个工具，而是用一个 `lsp` 工具承载 9 种操作，通过 `operation` 字段区分：

| 操作 | LSP 协议方法 | 说明 |
|---|---|---|
| `goToDefinition` | `textDocument/definition` | 查找符号定义位置 |
| `findReferences` | `textDocument/references` | 查找所有引用 |
| `hover` | `textDocument/hover` | 悬停信息（文档、类型） |
| `documentSymbol` | `textDocument/documentSymbol` | 文档符号大纲 |
| `workspaceSymbol` | `workspace/symbol` | 工作区全局符号搜索 |
| `goToImplementation` | `textDocument/implementation` | 查找接口/抽象方法实现 |
| `prepareCallHierarchy` | `textDocument/prepareCallHierarchy` | 准备调用层次 |
| `incomingCalls` | `callHierarchy/incomingCalls` | 谁调用了此函数 |
| `outgoingCalls` | `callHierarchy/outgoingCalls` | 此函数调用了谁 |

AI 模型只需理解这 9 种操作，底层 LSP 协议细节完全透明。

### 2. 语言无关：扩展名路由

LSP Tool 核心代码不包含任何语言特定逻辑。多语言支持通过**文件扩展名 → LSP 服务器**的路由实现：

```
.ts/.tsx → typescript-language-server
.go      → gopls
.py      → pyright-langserver
.rs      → rust-analyzer
```

路由表维护在 `extensionMap` 结构中，初始化时从服务器配置的 `Extensions` 字段自动构建。

添加新语言只需注册一个新的 `LspServerConfig`，无需修改任何核心代码。

### 3. 防御性编程：纵深防御

LSP 服务器可能返回格式不符或 `uri` 为 `undefined` 的数据。代码在**每层**都做过滤：

```
原始 LSP 响应
  → tool.go:       filterGitIgnored 过滤 gitignore 文件
  → tool.go:       filterValidLocations 再次过滤无效 location
  → formatters.go: 格式化函数第三次防御性过滤
```

任何一层失败都不会导致崩溃，而是优雅降级为错误提示。

### 4. 坐标系转换：1-based ↔ 0-based

AI 模型看到的是编辑器坐标系（1-based），而 LSP 协议使用 0-based。转换对 AI 完全透明：

- **输入层**（`GetMethodAndParams`）：`line - 1, character - 1`
- **输出层**（`FormatLocationStr`）：`line + 1, character + 1`

### 5. 惰性启动

LSP 子进程不会在 Agent 启动时全部拉起。管理器初始化只是加载配置、构建路由表，实际进程启动延迟到首次请求时：

```
用户对 .go 文件操作 → 首次触发 → 启动 gopls 子进程 → 初始化握手 → 发送请求
用户对 .py 文件操作 → 首次触发 → 启动 pyright 子进程 → 初始化握手 → 发送请求
```

避免为不使用的语言启动无人使用的后台进程。

---

## 架构分层

```
┌────────────────────────────────────────────────────────────────┐
│                    tools/lsp_tools.go                           │
│            LSPToolWrapper (Agent Tool 接口)                     │
│    实现 Name() / Description() / Parameters() / Execute()       │
└────────────────────────────┬───────────────────────────────────┘
                             │
┌────────────────────────────▼───────────────────────────────────┐
│                     tools/lsp/tool.go                           │
│                 ExecuteLSPOperation()                           │
│    10 步执行管道：验证 → 路径解析 → 文件检查 → 管理器获取       │
│    → 文件打开 → 方法映射 → 发送请求 → 两步编排                 │
│    → gitignore 过滤 → 格式化 → 返回统计                         │
└───────┬──────────────────────┬──────────────────────┬──────────┘
        │                      │                      │
┌───────▼──────────┐  ┌────────▼──────────┐  ┌───────▼──────────┐
│  manager.go      │  │  instance.go      │  │  formatters.go   │
│  Manager         │  │  ServerInstance   │  │  9 种格式化函数   │
│                  │  │                  │  │                   │
│  - 扩展名路由     │  │  - 生命周期管理   │  │  - GoToDefinition │
│  - 惰性启动       │  │  - 状态机         │  │  - FindReferences │
│  - 文件同步       │  │  - 重试逻辑       │  │  - Hover          │
│  - gitignore     │  │  - 健康检查       │  │  - DocumentSymbol │
│  - URI/路径转换   │  │                  │  │  - WorkspaceSymbol│
└───────┬──────────┘  └────────┬──────────┘  │  - CallHierarchy  │
        │                      │              └──────────────────┘
┌───────▼──────────────────────▼──────────┐
│            client.go                     │
│            LSPClient                     │
│                                          │
│  - JSON-RPC 2.0 通信                     │
│  - Content-Length header 协议             │
│  - 子进程管理 (stdin/stdout pipe)          │
│  - 请求/响应/通知分发                     │
│  - 30s 请求超时                           │
└──────────────────────────────────────────┘
```

---

## 核心数据结构

### LSPToolInput / LSPToolOutput

```go
type LSPToolInput struct {
    Operation Operation // 操作类型
    FilePath  string    // 文件路径
    Line      int       // 1-based 行号
    Character int       // 1-based 列号
}

type LSPToolOutput struct {
    Operation   Operation // 回显操作类型
    Result      string    // 格式化结果文本
    FilePath    string    // 回显文件路径
    ResultCount int       // 结果数量
    FileCount   int       // 涉及文件数
}
```

### LspServerConfig

```go
type LspServerConfig struct {
    Name                string            // "gopls", "typescript"
    Command             string            // 启动命令
    Args                []string          // 命令行参数
    Extensions          []string          // [".go"], [".ts", ".tsx"]
    ExtensionToLanguage map[string]string // ".ts" → "typescript"
    WorkspaceFolder     string            // 工作区根目录
    InitializationOptions interface{}     // 服务器初始化选项
    StartupTimeoutMs    int               // 启动超时
    Env                 map[string]string // 额外环境变量
}
```

### 状态机

```
ServerState:
  stopped → starting → running
      ↓                  ↓
    error  ←──────────── error
```

`ServerInstance` 维护状态机，包含崩溃恢复计数限制（默认最多 3 次重试）。

---

## 关键流程

### 10 步执行管道 (`ExecuteLSPOperation`)

```
Step 1: 验证 operation 合法性
Step 2: 解析文件绝对路径
Step 3: 验证文件存在、是普通文件
Step 4: 获取全局 LSP 管理器
Step 5: 确保文件已 didOpen（惰性打开、10MB 限制、languageId 透传）
Step 6: 映射 operation → LSP 方法名 + 参数
Step 7: 发送 LSP 请求（内含扩展名路由 + 惰性启动）
Step 8: 对 incomingCalls/outgoingCalls 执行两步编排
Step 9: 对位置相关结果执行 gitignore 过滤
Step 10: 格式化结果 + 统计计数
```

### Call Hierarchy 两步编排

`incomingCalls` 和 `outgoingCalls` 需要两次 LSP 请求，工具自动编排，对 AI 表现为单次操作：

```
Step 1: textDocument/prepareCallHierarchy
          → CallHierarchyItem[]（获取调用层次项）

Step 2: callHierarchy/incomingCalls { item: firstItem }
          或
        callHierarchy/outgoingCalls { item: firstItem }
          → CallHierarchyIncomingCall[] / CallHierarchyOutgoingCall[]
```

### ContentModified 重试

`rust-analyzer` 等服务器在项目索引期间会返回 `-32801` 错误码（Content Modified）。`ServerInstance.SendRequest()` 对此使用指数退避自动重试：

```
attempt 0: 立即发送
attempt 1: 等待 500ms 后重试
attempt 2: 等待 1s 后重试
attempt 3: 等待 2s 后重试（最后一次）
```

非 `-32801` 错误立即抛出，不浪费重试。

### gitignore 过滤

`FilterGitIgnored()` 对返回大量位置列表的操作（findReferences、workspaceSymbol 等）进行 gitignore 感知过滤：

1. 从 location 列表中提取所有唯一文件路径
2. 分批（每批 50 个）调用 `git check-ignore`
3. 过滤掉被忽略路径对应的 location
4. 对 `workspaceSymbol` 还额外处理 `SymbolInformation` 结构

---

## JSON-RPC 通信层

### 协议格式

LSP 使用自定义的 header + body 协议：

```
Content-Length: <字节数>\r\n
\r\n
<JSON 消息体>
```

### 消息类型

- **请求（Request）**：有 `id` 字段，期待响应
- **响应（Response）**：包含 `id` 和 `result`/`error`
- **通知（Notification）**：无 `id` 字段，无响应

### 并发模型

- `sendRequest()`：同步等待响应（带 30s 超时），通过 channel 关联请求-响应
- `sendNotification()`：fire-and-forget，立即返回
- `readLoop()`：后台 goroutine 持续读取服务器 stdout，通过 `dispatchMessage()` 分发到对应 channel 或 handler

### 子进程管理

```
os/exec.Command → stdin pipe (写请求) → stdout pipe (读响应)
                                      ↕
                              readLoop goroutine
```

停止时先 Kill 子进程，再关闭管道。

---

## 多语言扩展

### 内置配置

| 语言 | 命令 | 扩展名 | languageId |
|---|---|---|---|
| Go | `gopls` | `.go` | `go` |
| TypeScript | `typescript-language-server --stdio` | `.ts`, `.tsx` | `typescript`, `typescriptreact` |
| JavaScript | 同上 | `.js`, `.jsx`, `.mjs`, `.cjs` | `javascript`, `javascriptreact` |
| Python | `pyright-langserver --stdio` | `.py`, `.pyi` | `python` |
| Rust | `rust-analyzer` | `.rs` | `rust` |

### 动态注册

```go
mgr := lsp.GetGlobalManager()
mgr.RegisterServer(lsp.LspServerConfig{
    Name:    "clangd",
    Command: "clangd",
    Extensions: []string{".c", ".cpp", ".h"},
    ExtensionToLanguage: map[string]string{
        ".c": "c", ".cpp": "cpp", ".h": "c",
    },
})
```

### 插件化路径

当前配置通过 `DefaultServerConfigs()` 硬编码。要支持插件化，可将配置改为从 JSON 文件或插件接口加载——`Manager.Initialize()` 接受的 `[]LspServerConfig` 参数已为此预留。

---

## 错误处理策略

| 场景 | 处理方式 |
|---|---|
| 文件不存在 | 返回 error（上层 Agent 感知） |
| 文件类型无服务器 | 返回 Output（result 为错误描述，不抛 error） |
| LSP 请求失败 | 返回 Output + 错误描述，标记服务器为 error |
| 管理器未初始化 | 返回 Output + 提示信息 |
| 文件超过 10MB | 返回 Output + 大小提示 |
| 服务器崩溃 | 状态标记为 error，下次请求自动重试（最多 3 次） |
| 初始化超时 | 清理子进程，状态标记为 error |
| LSP 返回空结果 | formatter 返回友好的 "No X found" 消息 |

关键设计原则：**验证性错误抛 error，运行时错误优雅降级为 Output**。

---

## 文件清单

| 文件 | 行数 | 职责 |
|---|---|---|
| `types.go` | ~231 | Operation 枚举、LSP 协议类型（Position/Range/Location 等） |
| `config.go` | ~122 | LspServerConfig、扩展名路由表构建、内置配置 |
| `client.go` | ~440 | JSON-RPC 2.0 通信、Content-Length 协议、进程管理 |
| `instance.go` | ~245 | 服务器生命周期、状态机、健康检查、ContentModified 重试 |
| `manager.go` | ~418 | 多服务器管理、扩展名路由、文件同步、gitignore 过滤 |
| `tool.go` | ~299 | 核心 10 步执行管道、Call Hierarchy 编排、全局管理器单例 |
| `formatters.go` | ~459 | 9 种操作的格式化函数、辅助工具 |
| `lsp_tools.go` | ~152 | Agent Tool 接口包装器 |

总计约 **2366 行**。
