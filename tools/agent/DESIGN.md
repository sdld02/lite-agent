# 子Agent 系统设计文档

## 一、设计目标

参考 Claude Code 的 AgentTool 设计，为 `lite-agent` 构建一个**Agent 即工具（Agent as a Tool）**的子Agent系统。子Agent 不是一个特殊的一级公民——它就是一个普通的 Tool，LLM 通过标准的 function calling 机制决定何时"委托任务"。

### 核心原则

1. **Agent 即工具** — AgentTool 实现 `agent.Tool` 接口，与 `FileReadTool`、`BashTool` 地位等同
2. **复用现有引擎** — 子Agent 直接复用 `agent.Agent` 结构和 `agent.LLMProvider` 接口
3. **工具池隔离** — 每个子Agent 拥有独立的工具集合，根据 Agent 定义过滤
4. **防无限递归** — 子Agent 默认禁止调用 `agent` 工具
5. **可扩展** — 支持内置Agent（Go代码）和自定义Agent（.md 文件）

---

## 二、架构概览

```
┌──────────────────────────────────────────────────────┐
│                     主 Agent (agent.Agent)              │
│  tools = [calculator, shell, file_read, ..., agent]  │
│  当 LLM 决定调用 agent 工具时：                          │
└──────────────────────────┬───────────────────────────┘
                           │ agent(description, prompt, subagent_type)
                           ▼
┌──────────────────────────────────────────────────────┐
│                    AgentTool (tool.go)                 │
│  1. 根据 subagent_type 查找 AgentDefinition            │
│  2. 通过 Runner 启动子Agent                            │
│  3. 等待子Agent 完成，收集结果                          │
│  4. 返回 tool_result 给主Agent                         │
└──────────────────────────┬───────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────┐
│                    Runner (runner.go)                  │
│  1. 创建新的 agent.Agent 实例                          │
│  2. 设置子Agent 专属的 system prompt                   │
│  3. 根据 AgentDefinition 过滤工具（resolveToolNames）    │
│  4. 执行子Agent.Run() → 同步等待完成                    │
│  5. 统计 tool_use 次数和耗时                           │
│  6. 返回 SubAgentResult                               │
└──────────────────────────┬───────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────┐
│              ToolRegistry (runner.go)                  │
│  维护 工具名 → 工厂函数 的映射                           │
│  ┌─────────────────────────────────────────────┐     │
│  │ "calculator"  → func() agent.Tool { ... }   │     │
│  │ "shell"       → func() agent.Tool { ... }   │     │
│  │ "file_read"   → func() agent.Tool { ... }   │     │
│  │ ...                                         │     │
│  └─────────────────────────────────────────────┘     │
└──────────────────────────────────────────────────────┘
```

---

## 三、数据模型

### 3.1 AgentDefinition — Agent 定义

```go
type AgentDefinition struct {
    AgentType       string      // 唯一类型标识，如 "general-purpose", "Explore"
    WhenToUse       string      // 告诉 LLM 何时使用此 Agent
    Source          AgentSource // 来源：built-in / user / project

    Tools           []string    // 允许的工具，["*"] = 所有工具
    DisallowedTools []string    // 禁止的工具列表

    SystemPrompt    string      // 子Agent 的系统提示词
    Model           string      // "inherit" = 继承父Agent 的模型

    PermissionMode  string      // acceptEdits / plan / bypassPermissions
    MaxTurns        int         // 最大对话轮数，0 = 默认 50
    Background      bool        // 是否强制后台运行

    Color           string      // Agent 颜色标记
    Filename        string      // 文件名（自定义Agent）
    BaseDir         string      // 文件所在目录

    Skills          []string    // 预加载的技能
    Memory          string      // 持久化记忆作用域
    Isolation       string      // 隔离模式：worktree
}
```

### 3.2 AgentSource — 定义来源

```
SourceBuiltIn  = "built-in"   // 硬编码在 Go 代码中
SourceUser     = "user"       // 从用户目录加载
SourceProject  = "project"    // 从项目目录加载
```

优先级（去重时保留最先匹配的）：
```
policySettings > flagSettings > projectSettings > userSettings > plugin > built-in
```

### 3.3 SubAgentResult — 运行结果

```go
type SubAgentResult struct {
    AgentID          string // 子Agent 唯一ID
    AgentType        string // 使用的 Agent 类型
    Content          string // 子Agent 的最终文本输出
    TotalToolUseCount int   // 工具调用总次数
    TotalDurationMs  int64  // 总耗时（毫秒）
    TotalTokens      int    // 总 token 消耗
}
```

---

## 四、核心模块

### 4.1 ToolRegistry — 工具注册表

**设计决策：为什么需要工厂函数？**

每个子Agent 需要独立的工具实例（而非共享主Agent 的工具实例）。这是因为：
- 子Agent 拥有独立的 `agent.Agent` 实例和 memory
- 工具可能包含状态（如文件缓存），需要隔离
- 不同的子Agent 使用不同的工具子集

```go
type ToolRegistry struct {
    factories map[string]ToolFactory  // 工具名 → 工厂函数
}

type ToolFactory func() agent.Tool
```

注册示例（在 main.go 中）：
```go
registry := tools.NewToolRegistry()
registry.Register("calculator", func() agent.Tool { return tools.NewCalculatorTool() })
registry.Register("shell",      func() agent.Tool { return tools.NewShellToolUnsafe() })
registry.Register("file_read",  func() agent.Tool { return tools.NewFileReadTool() })
// ...
```

### 4.2 Runner — 子Agent 执行引擎

执行流程：
```
Run(ctx, def, prompt)
  │
  ├── 1. agent.NewAgent(provider)          // 复用主Agent的LLM连接
  ├── 2. SetSystemPrompt(def.SystemPrompt) // 设置子Agent专属提示词
  ├── 3. SetMaxSteps(def.EffectiveMaxTurns()) // 设置最大轮数
  ├── 4. resolveToolNames(def)             // 根据定义过滤工具
  │      └── 通配符模式 (["*"])  → 所有工具 - disallowedTools - "agent"
  │      └── 白名单模式 ([...])  → 指定工具 - disallowedTools
  ├── 5. 逐个 Register 工具到子Agent
  ├── 6. subAgent.Run(ctx, prompt)        // 同步阻塞执行
  └── 7. 统计 tool_use 次数 + 耗时 → SubAgentResult
```

### 4.3 resolveToolNames — 工具解析规则

参考 Claude Code 的 `resolveAgentTools()`：

| 条件 | 行为 |
|------|------|
| `Tools == ["*"]` 或 `nil` | 所有已注册工具 - `DisallowedTools` - `"agent"` |
| `Tools == ["Read", "Grep"]` | 仅 `Read` 和 `Grep` - `DisallowedTools` - `"agent"` |
| 任何情况 | 总是禁止 `agent`（防递归） |

### 4.4 AgentTool — 工具实现

AgentTool 实现 `agent.Tool` 接口的四个方法：

```
Name()        → "agent"
Description() → 动态生成，列出所有可用 Agent 类型及其工具
Parameters()  → { description, prompt, subagent_type (enum) }
Execute()     → 查找定义 → Runner.Run() → 格式化 JSON 输出
```

**动态 Description 示例：**
```
Launch a new agent to handle complex, multi-step tasks autonomously.

Available agent types and the tools they have access to:
- general-purpose: General-purpose agent for... (Tools: All tools)
- Explore: Fast agent specialized for... (Tools: All tools except file_write, file_edit)
- Plan: Software architect agent for... (Tools: All tools except file_write, file_edit)
```

---

## 五、内置 Agent 定义

### 5.1 general-purpose（通用Agent）

| 属性 | 值 |
|------|------|
| Tools | `["*"]` 所有工具 |
| DisallowedTools | 无（但 `agent` 被 runner 自动禁用） |
| 设计意图 | 处理复杂的多步骤研究/实现任务 |
| 系统提示词要点 | 搜索代码、分析架构、彻底完成任务、不创建.md文件 |

### 5.2 Explore（只读探索Agent）

| 属性 | 值 |
|------|------|
| Tools | `nil`（默认所有） |
| DisallowedTools | `["agent", "file_write", "file_edit"]` |
| 设计意图 | 快速代码库搜索和探索，严格只读 |
| 系统提示词要点 | CRITICAL: READ-ONLY MODE，禁止创建/修改/删除文件 |

### 5.3 Plan（只读规划Agent）

| 属性 | 值 |
|------|------|
| Tools | `nil`（默认所有） |
| DisallowedTools | `["agent", "file_write", "file_edit"]` |
| 设计意图 | 软件架构分析和实施计划设计 |
| 系统提示词要点 | 要求输出 `### Critical Files for Implementation` 文件列表 |

---

## 六、自定义 Agent 加载

### 6.1 Markdown 文件格式

参考 Claude Code 的 agent markdown 格式：

```markdown
---
name: my-code-reviewer
description: Review code for security issues and best practices
tools: file_read, shell
model: inherit
color: "#ff6b6b"
maxTurns: 30
---

You are a security-focused code reviewer. Your role is to...

## Process
1. Read the changed files
2. Check for common security vulnerabilities
3. Report findings
```

### 6.2 支持的 frontmatter 字段

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `name` | string | ✅ | Agent 类型标识 |
| `description` | string | ✅ | 何时使用此 Agent |
| `tools` | string | ❌ | 逗号分隔的工具列表，`*` = 所有 |
| `disallowedTools` | string | ❌ | 禁止的工具列表 |
| `model` | string | ❌ | `inherit` 表示继承父Agent |
| `maxTurns` | int | ❌ | 最大对话轮数 |
| `color` | string | ❌ | 颜色标记 |

### 6.3 加载目录

```
~/.lite-agent/agents/     ← 用户级 Agent
.lite-agent/agents/       ← 项目级 Agent
```

通过 `LoadAgentsFromDir()` 函数加载目录下所有 `.md` 文件。

---

## 七、与 Claude Code 的对比

| 维度 | Claude Code | lite-agent（本实现） |
|------|-------------|---------------------|
| 语言 | TypeScript | Go |
| **核心哲学** | Agent 即 Tool | Agent 即 Tool ✅ |
| **工具注册** | `buildTool()` 声明式工厂 | `ToolRegistry` + 工厂函数 |
| **内置 Agent** | 6个（general/Explore/Plan/verification/guide/statusline） | 3个（general/Explore/Plan） |
| **自定义Agent** | 从 .md frontmatter 加载 | 从 .md frontmatter 加载 ✅ |
| **工具过滤** | `resolveAgentTools()` 多层过滤 | `resolveToolNames()` 两层过滤 |
| **异步执行** | 支持 sync/async/auto-background | 当前仅 sync |
| **Fork 机制** | 继承父Agent上下文 + prompt cache 共享 | 未实现（后续可扩展） |
| **Worktree 隔离** | git worktree 可选隔离 | 未实现（后续可扩展） |
| **MCP 支持** | Agent可定义专属 MCP 服务器 | 未实现（后续可扩展） |
| **权限模式** | bubble/acceptEdits/bypassPermissions/plan | 未实现（后续可扩展） |

---

## 八、数据流示例

### 8.1 用户请求："帮我探索项目结构，找到所有 API 端点"

```
用户输入
  ↓
主Agent 分析 → 决定使用 Explore Agent
  ↓
调用 agent(
  description: "探索API端点",
  prompt: "搜索项目中所有HTTP API端点定义。使用code_probe获取结构，shell grep搜索路由注册模式...",
  subagent_type: "Explore"
)
  ↓
AgentTool.Execute()
  → 找到 Explore AgentDefinition
  → Runner.Run()
    → 创建子 agent.Agent (system prompt = ExploreSystemPrompt)
    → 注册工具: calculator, system_info, shell, file_read, file_diff,
                code_probe, code_stats, lsp, task_*
    → (不注册: agent, file_write, file_edit)
    → subAgent.Run() → LLM 循环执行工具调用
    → 子Agent 返回结果: "找到以下API端点: ..."
  → 返回 SubAgentResult
  ↓
主Agent 收到 tool_result → 向用户展示子Agent的发现
```

### 8.2 工具过滤验证

```
Explore Agent 尝试调用 file_write("test.txt", "hello")
  ↓
file_write 在 DisallowedTools 中
  ↓
resolveToolNames() 已排除 file_write
  ↓
子Agent 的 tools map 中没有 "file_write"
  ↓
LLM 收到 "unknown tool: file_write" 错误
  ↓
LLM 转而使用 file_read 等允许的工具
```

---

## 九、扩展方向

### 9.1 异步子Agent

当前子Agent 同步阻塞主Agent。可扩展为：
```go
type Runner struct {
    asyncTasks map[string]*SubAgentInstance
}

func (r *Runner) RunAsync(ctx context.Context, def AgentDefinition, prompt string) (string, error) {
    // 立即返回 async_launched，后台执行
}
```

### 9.2 Fork 子Agent（上下文继承）

参考 Claude Code 的 fork 机制：
- 子Agent 继承父Agent 的完整对话上下文
- 共享 prompt cache 以降低延迟和成本
- 通过 `<fork-boilerplate>` 标记防止递归 fork

### 9.3 Worktree 隔离

```go
if def.Isolation == "worktree" {
    worktreePath := createGitWorktree(agentID)
    runWithWorktree(worktreePath, func() {
        runner.Run(ctx, def, prompt)
    })
    cleanupWorktree(worktreePath)
}
```

### 9.4 权限模式

```go
type PermissionMode string
const (
    AcceptEdits       PermissionMode = "acceptEdits"
    Plan              PermissionMode = "plan"
    BypassPermissions PermissionMode = "bypassPermissions"
    Bubble            PermissionMode = "bubble"  // 弹出到父Agent
)
```

### 9.5 Agent 记忆

```go
type AgentMemory struct {
    Scope   string // "user", "project", "local"
    Content string
}

// 在子Agent 的 system prompt 中注入记忆上下文
```

---

## 十、文件清单

```
tools/agent/
├── definition.go      # 119 行   Agent定义类型 + SubAgentResult
├── runner.go          # 164 行   子Agent运行器 + ToolRegistry
├── tool.go            # 170 行   AgentTool（实现 agent.Tool 接口）
├── loader.go          # 181 行   .md 文件加载器 + frontmatter 解析
├── builtin/
│   ├── general.go     # 32 行    general-purpose Agent
│   ├── explore.go     # 48 行    Explore（只读探索）Agent
│   └── plan.go        # 63 行    Plan（只读规划）Agent

tools/agent_tools.go   # 30 行    工具注册入口
main.go                # +25 行    ToolRegistry 初始化 + AgentTool 注册
```

总计约 **777 行** Go 代码，零外部依赖，`go build ./...` + `go vet ./...` 零警告通过。
