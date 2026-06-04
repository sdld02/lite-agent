# Go AI Agent 框架

一个用 Go 语言实现的 AI Agent 框架，支持工具调用、子 Agent 协作、技能系统、MCP 协议集成和多种交互模式。

## 项目结构

```
lite-agent/
├── agent/                          # Agent 核心包
│   └── agent.go                    # Agent 结构、Tool 接口、StreamEvent 事件模型
├── llm/                            # LLM 提供者
│   └── openai.go                   # OpenAI 兼容 API（支持流式 + 非流式）
├── mcp/                            # MCP 协议客户端
│   ├── client.go                   # MCP 客户端实现
│   ├── config.go                   # mcp.json 配置加载
│   ├── manager.go                  # MCP 连接管理器（按需启动）
│   ├── transport.go                # stdio 传输层
│   └── types.go                    # 类型定义
├── server/                         # WebSocket 服务
│   ├── handler.go                  # 连接处理器（多会话独立工具实例）
│   ├── protocol.go                 # 消息协议（16 种服务端 + 17 种客户端消息类型）
│   ├── server.go                   # 服务主控
│   └── static/index.html           # Web 控制面板
├── session/                        # 会话持久化
│   ├── session.go                  # 会话模型
│   └── store.go                    # JSON 文件存储（原子写入）
├── bot/                            # Telegram Bot 集成
│   └── telegram.go                 # Telegram Bot API 适配
├── tools/                          # 工具系统
│   ├── agent/                      # 子 Agent 系统
│   │   ├── builtin/                # explore.go, general.go, plan.go
│   │   ├── definition.go           # Agent 类型定义
│   │   ├── loader.go               # Agent 定义加载器
│   │   ├── runner.go               # 子 Agent 执行器
│   │   ├── tool.go                 # AgentTool（对外工具接口）
│   │   └── DESIGN.md               # 设计文档
│   ├── code/                       # 代码分析
│   │   ├── probe.go                # 项目结构探查
│   │   └── stats.go                # 代码行数统计
│   ├── file/                       # 文件操作
│   │   ├── diff.go                 # 文件差异比较
│   │   ├── edit.go                 # 精确字符串替换
│   │   ├── read.go                 # 文件读取（分页/多种模式）
│   │   └── write.go                # 文件写入
│   ├── lsp/                        # LSP 代码智能
│   │   ├── client.go, config.go, instance.go, manager.go
│   │   ├── tool.go, types.go, formatters.go, DESIGN.md
│   ├── skill/                      # 技能系统
│   │   ├── builtin.go              # 内置技能（commit, review-pr 等）
│   │   ├── constants.go, loader.go, runner.go
│   │   ├── tool.go, types.go
│   ├── task/                       # 任务管理系统
│   │   ├── create.go, get.go, list.go, update.go
│   │   ├── manager.go, store.go, types.go
│   │   └── DESIGN.md
│   ├── agent_tools.go              # 子 Agent 工具注册
│   ├── ask_user_question.go        # 执行中向用户提问
│   ├── builtin.go                  # calculator, system_info, current_time
│   ├── code_tools.go               # code_probe, code_stats 包装
│   ├── file_tools.go               # file_edit/write/diff/read 包装
│   ├── glob.go                     # 文件名模式匹配（纯 Go）
│   ├── grep.go                     # 代码搜索（纯 Go，正则/glob/分页）
│   ├── lsp_tools.go                # LSP 工具包装
│   ├── mcp_tools.go                # MCP 工具包装
│   ├── shell.go                    # Shell 命令执行
│   ├── skill_tools.go              # Skill 工具注册
│   ├── task_tools.go               # Task 工具注册
│   ├── webfetch.go                 # 网页抓取 + AI 分析
│   └── websearch.go                # DuckDuckGo 网页搜索
├── docs/
│   └── websocket-server-design.md  # WebSocket 服务设计文档
├── main.go                         # 程序入口（CLI/WebSocket/Telegram 三种模式）
├── go.mod / go.sum
└── README.md
```

## 快速开始

### 环境变量方式

```bash
# Linux/Mac - 使用 DeepSeek
export OPENAI_API_KEY="your-deepseek-api-key"
export OPENAI_BASE_URL="https://api.deepseek.com/v1"
export OPENAI_MODEL="deepseek-chat"
go run main.go
```

```powershell
# Windows PowerShell
$env:OPENAI_API_KEY="your-deepseek-api-key"
$env:OPENAI_BASE_URL="https://api.deepseek.com/v1"
$env:OPENAI_MODEL="deepseek-chat"
go run main.go
```

### 命令行参数方式

```bash
# 使用预设提供者
go run main.go -provider=deepseek -key=your-api-key

# 使用 OpenAI
go run main.go -provider=openai -key=your-api-key

# 使用本地 Ollama
go run main.go -provider=ollama -key=ollama

# 自定义 URL 和模型
go run main.go -url=https://api.deepseek.com/v1 -model=deepseek-chat -key=your-api-key

# 关闭流式输出（默认开启）
go run main.go -provider=deepseek -key=your-api-key -stream=false
```

### 三种启动模式

| 模式 | 命令 | 说明 |
|------|------|------|
| **CLI 交互式** | `go run main.go -provider=deepseek -key=xxx` | 终端交互式对话（默认） |
| **WebSocket 服务** | `go run main.go -server -addr=:9090` | Web 控制面板 + WebSocket API |
| **Telegram Bot** | `go run main.go -telegram -token=xxx` | 通过 Telegram 聊天使用 Agent |

### CLI 交互命令

在对话过程中，支持以下命令：

| 命令 | 说明 |
|------|------|
| `quit` / `exit` | 退出程序 |
| `prompt` | 查看完整的系统提示词 |
| `sessions` | 查看所有历史会话 |
| `new` | 开始新会话 |
| `load <id>` | 加载指定会话 |
| `delete <id>` | 删除指定会话 |

## 核心概念

### 1. Agent 核心结构 (`agent/agent.go`)

```go
type Agent struct {
    provider     LLMProvider        // LLM 提供者
    tools        map[string]Tool    // 可用工具集合
    memory       []Message          // 对话记忆
    systemPrompt string             // 系统提示词
    maxSteps     int                // 最大执行步数
}
```

### 2. 工具接口

```go
type Tool interface {
    Name() string                              // 工具名称
    Description() string                       // 工具描述
    Parameters() map[string]interface{}        // 参数定义 (JSON Schema)
    Execute(ctx, args) (*ToolResult, error)    // 执行逻辑
}
```

### 3. 统一流事件模型

Agent 支持 6 种流事件类型，通过统一的 `StreamEventHandler` 回调处理：

| 事件类型 | 说明 |
|----------|------|
| `EventContent` | 正文文本片段（逐字输出） |
| `EventReasoning` | 推理模型的思考过程（灰色展示） |
| `EventToolCallProgress` | 工具调用参数生成进度 |
| `EventFlush` | 工具调用前的文本刷新（替换为 Markdown 渲染） |
| `EventToolCallStart` | 工具开始执行 |
| `EventToolCallEnd` | 工具执行完毕（含结果） |

```go
type StreamEvent struct {
    Type       StreamEventType
    Content    string                 // EventContent/Reasoning/Flush: 文本
    ToolName   string                 // 工具名称
    ArgsBytes  int                    // 参数生成进度
    ToolArgs   map[string]interface{} // 工具参数
    ToolResult *ToolResult            // 工具执行结果
}

type StreamEventHandler func(event StreamEvent)
```

### 4. ToolResult — 双通道设计

工具执行结果分离为 LLM 文本和 UI 富数据：

```go
type ToolResult struct {
    Content  string      // 给 LLM 的精简文本
    RichData interface{} // 给 UI/WebSocket 的完整结构体
    IsError  bool        // 是否为错误
}
```

### 5. 执行流程

```
用户输入 → LLM 推理 → 判断是否需要工具调用
                ↓              ↓
              返回结果      执行工具 → 结果返回 LLM → 继续推理
```

## 工具参考

### 系统工具

| 工具 | 说明 |
|------|------|
| `calculator` | 纯 Go 实现的数学表达式求值器，支持 sin/cos/sqrt 等函数 |
| `system_info` | 获取操作系统、架构、CPU 核心数等信息 |
| `shell` | Shell 命令执行，支持安全白名单、超时后台化 |

### 文件操作

| 工具 | 说明 |
|------|------|
| `file_read` | 文件读取，支持分页 (offset/max_lines)、head_lines、tail_lines、多编码 |
| `file_write` | 文件写入（创建/覆盖），支持 UTF-8/GBK/Latin-1 等编码 |
| `file_edit` | 精确字符串替换，支持 dry_run 预览、批量 edits、按行号替换 |
| `file_diff` | 文件差异比较（unified/simple/html 格式） |

### 代码分析

| 工具 | 说明 |
|------|------|
| `code_probe` | 项目结构探查，支持 summary/structure/flat/grouped/tree/recent 模式 |
| `code_stats` | 代码行数统计，按语言分组，支持并发统计 |
| `grep` | 纯 Go 代码搜索，支持正则、glob 过滤、多行模式、分页 |
| `glob` | 文件名模式匹配，支持 `**` 递归、按修改时间排序 |
| `lsp` | LSP 代码智能（9 种操作，见下文） |

### Web 与外部集成

| 工具 | 说明 |
|------|------|
| `web_search` | DuckDuckGo 网页搜索 |
| `web_fetch` | URL 抓取 + AI 内容分析 |
| `mcp` | MCP 协议工具调用（按需连接外部工具服务器） |

### Agent 协作

| 工具 | 说明 |
|------|------|
| `agent` | 启动子 Agent 处理复杂任务（支持 general-purpose / Explore / Plan 三种类型） |
| `task_create` | 创建任务 |
| `task_update` | 更新任务状态 |
| `task_list` | 列出所有任务 |
| `task_get` | 获取任务详情 |
| `skill` | 调用技能（斜杠命令），支持 inline/fork 两种执行模式 |
| `ask_user_question` | 在任务执行中向用户发起多选题 |

## 子 Agent 系统

子 Agent 系统允许 Agent 将复杂多步骤任务委派给专门的子 Agent 执行：

| 类型 | 说明 | 可用工具 |
|------|------|----------|
| `general-purpose` | 通用 Agent，处理复杂的多步骤任务 | 全部工具 |
| `Explore` | 代码库探索 Agent（只读） | 除 agent/file_write/file_edit 外的所有工具 |
| `Plan` | 架构设计 Agent（只读） | 除 agent/file_write/file_edit 外的所有工具 |

子 Agent 在隔离的子进程中执行，完成后将结果返回给主 Agent 继续处理。

## 技能系统

技能系统提供可复用的领域知识和操作流程。通过 `/skill-name` 或 `skill` 工具调用。

### 内置技能

| 技能 | 说明 | 执行模式 |
|------|------|----------|
| `commit` | 从 staged changes 生成规范的 git commit 信息 | fork |
| `review-pr` | 审查 Pull Request 并提供可操作的反馈 | fork |
| `explain-code` | 详细解释代码的工作原理 | inline |
| `plan` | 进入规划模式，分析任务并创建详细计划 | inline |

### 执行模式

- **inline**：在当前对话中展开技能提示词，LLM 直接在上下文中执行
- **fork**：启动独立子 Agent 隔离执行，完成后返回结果

### 自定义技能

在项目根目录创建 `.claude/skills/` 目录，添加 Markdown 文件即可定义新技能。技能文件包含 YAML 前置元数据和技能提示词。

## MCP 协议集成

MCP (Model Context Protocol) 让 Agent 能够使用外部工具服务。通过 `mcp.json` 配置文件定义服务器：

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/allowed/files"]
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "<your-token>"
      }
    }
  }
}
```

配置文件查找顺序：项目根目录 `mcp.json` → 用户主目录 `~/.lite-agent/mcp.json`。

MCP 服务器按需启动——只有首次调用时才启动子进程，减少资源消耗。

## LSP 代码智能

通过 Language Server Protocol 提供代码智能功能，支持 9 种操作：

| 操作 | 说明 |
|------|------|
| `goToDefinition` | 跳转到符号定义 |
| `findReferences` | 查找所有引用 |
| `hover` | 获取悬停文档和类型信息 |
| `documentSymbol` | 获取文档符号列表 |
| `workspaceSymbol` | 搜索工作区符号 |
| `goToImplementation` | 查找接口实现 |
| `prepareCallHierarchy` | 调用层次入口 |
| `incomingCalls` | 谁调用了此函数 |
| `outgoingCalls` | 此函数调用了谁 |

支持的语言：Go (gopls)、TypeScript/JavaScript (typescript-language-server)、Python (pyright)、Rust (rust-analyzer)。

## 会话持久化

会话自动保存到 `~/.lite-agent/sessions/` 目录。支持：

- 自动恢复上次会话
- 列出、加载、删除历史会话
- 原子写入（tmp + rename）
- 命令行管理：`sessions`、`new`、`load <id>`、`delete <id>`

## 支持的 LLM 提供者

| 提供者 | Base URL | 默认模型 | 获取 API Key |
|--------|----------|----------|-------------|
| OpenAI | https://api.openai.com/v1 | gpt-4o | [OpenAI](https://platform.openai.com/) |
| DeepSeek | https://api.deepseek.com/v1 | deepseek-v4-pro | [DeepSeek](https://platform.deepseek.com/) |
| Moonshot | https://api.moonshot.cn/v1 | moonshot-v1-8k | [Kimi](https://platform.moonshot.cn/) |
| 智谱 AI | https://open.bigmodel.cn/api/paas/v4 | glm-4 | [智谱](https://open.bigmodel.cn/) |
| 通义千问 | https://dashscope.aliyuncs.com/compatible-mode/v1 | qwen-turbo | [阿里云](https://dashscope.console.aliyun.com/) |
| Ollama | http://localhost:11434/v1 | llama2 | 本地运行 |

**注意：所有国内模型的 API 格式与 OpenAI 完全兼容，可直接替换使用。**

通过环境变量 `OPENAI_BASE_URL` 和 `OPENAI_MODEL` 可以覆盖默认配置。

## 添加自定义工具

### 方式一：实现 Tool 接口

```go
type MyTool struct{}

func (t *MyTool) Name() string        { return "my_tool" }
func (t *MyTool) Description() string { return "这是一个自定义工具" }

func (t *MyTool) Parameters() map[string]interface{} {
    return map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "input": map[string]interface{}{
                "type": "string", "description": "输入参数",
            },
        },
        "required": []string{"input"},
    }
}

func (t *MyTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
    input := args["input"].(string)
    return &agent.ToolResult{Content: "处理结果: " + input}, nil
}

// 注册
ag.AddTool(&MyTool{})
```

### 方式二：Shell 工具安全白名单

```go
// 安全模式（默认）- 只允许白名单命令
ag.AddTool(tools.NewShellTool())

// 无限制模式（慎用！）
ag.AddTool(tools.NewShellToolUnsafe())

// 动态添加白名单命令
shellTool := tools.NewShellTool()
shellTool.AddAllowedCommand("rm")
ag.AddTool(shellTool)
```

Shell 默认白名单包含 40+ 个常用命令（`ls`, `git`, `go`, `npm`, `docker`, `curl` 等），同时覆盖 Linux/Mac 原始命令和 Windows/PowerShell 等价命令。

## 核心学习点

1. **工具调用流程**：LLM 如何决定调用哪个工具、如何传递参数
2. **多轮对话**：Agent 如何保持上下文记忆
3. **循环执行**：工具执行结果如何反馈给 LLM 继续推理
4. **流式输出**：统一 StreamEvent 事件模型、SSE 协议解析、终端 Markdown 渲染
5. **子 Agent 协作**：任务委派、隔离执行、结果汇总
6. **技能系统**：inline vs fork 执行模式、自定义技能加载
7. **MCP 协议**：按需连接、stdio 传输、工具发现与调用
8. **LSP 集成**：多语言代码智能、惰性初始化

## 扩展方向

- [x] 流式输出支持
- [x] Web 搜索工具（`web_search` + `web_fetch`）
- [x] 对话持久化
- [x] 子 Agent 系统（general-purpose / Explore / Plan）
- [x] 技能系统（commit / review-pr / explain-code / plan）
- [x] 任务管理（create / update / list / get）
- [x] 文件操作（read / write / edit / diff）
- [x] 代码分析（code_probe / code_stats / grep / glob）
- [x] LSP 代码智能（9 种操作）
- [x] MCP 协议集成
- [x] WebSocket 服务 + Web 控制面板
- [x] Telegram Bot 集成
- [x] 用户交互式提问（`ask_user_question`）
- [ ] 向量数据库支持（RAG）
- [ ] 多 Agent 协作（多主 Agent）
- [ ] 代码执行沙箱
