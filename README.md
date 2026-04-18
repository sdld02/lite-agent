# Go AI Agent 学习框架

这是一个用 Go 语言实现的 AI Agent 框架，用于学习 Agent 的核心原理。

## 项目结构

```
lite-agent/
├── agent/                    # Agent 核心包
│   └── agent.go             # Agent 核心逻辑（类型定义、Agent 结构、执行循环）
├── llm/
│   └── openai.go            # OpenAI 兼容 API 实现（LLMProvider）
├── tools/
│   ├── builtin.go           # 内置工具集合（calculator、system_info）
│   └── shell.go             # Shell 命令工具
├── main.go                   # 程序入口（交互式对话）
├── go.mod
├── go.sum
└── README.md
```

## 核心概念

### 1. Agent 核心结构 (`agent/agent.go`)

```go
type Agent struct {
    provider LLMProvider    // LLM 提供者（OpenAI、DeepSeek 等）
    tools    map[string]Tool // 可用工具集合
    memory   []Message       // 对话记忆
    maxSteps int             // 最大执行步数
}
```

### 2. 工具接口 (`agent/agent.go`)

```go
type Tool interface {
    Name() string                              // 工具名称
    Description() string                        // 工具描述
    Parameters() map[string]interface{}        // 参数定义 (JSON Schema)
    Execute(ctx, args) (string, error)         // 执行逻辑
}
```

### 3. 执行流程

```
用户输入 → LLM 推理 → 判断是否需要工具调用
                ↓              ↓
              返回结果      执行工具 → 结果返回 LLM → 继续推理
```

### 4. LLM 工具调用格式

当 LLM 决定调用工具时，返回格式如下：

```json
{
  "role": "assistant",
  "content": null,
  "tool_calls": [
    {
      "id": "call_abc123",
      "type": "function",
      "function": {
        "name": "shell",
        "arguments": "{\"command\": \"ls -la\"}"
      }
    }
  ]
}
```

Agent 检测到 `tool_calls` 字段后，执行工具并将结果返回给 LLM 继续推理。

## 支持的 LLM 提供者

| 提供者 | Base URL | Model 示例 | 获取 API Key |
|--------|----------|------------|--------------|
| OpenAI | https://api.openai.com/v1 | gpt-4o, gpt-4-turbo | [OpenAI](https://platform.openai.com/) |
| DeepSeek | https://api.deepseek.com/v1 | deepseek-chat, deepseek-coder | [DeepSeek](https://platform.deepseek.com/) |
| Moonshot | https://api.moonshot.cn/v1 | moonshot-v1-8k | [Kimi](https://platform.moonshot.cn/) |
| 智谱 AI | https://open.bigmodel.cn/api/paas/v4 | glm-4 | [智谱](https://open.bigmodel.cn/) |
| 通义千问 | https://dashscope.aliyuncs.com/compatible-mode/v1 | qwen-turbo | [阿里云](https://dashscope.console.aliyun.com/) |
| Ollama 本地 | http://localhost:11434/v1 | llama2, qwen2 | 本地运行 |

**注意：DeepSeek、Moonshot、智谱、通义千问 等国内模型的 API 格式与 OpenAI 完全兼容！**

## 快速开始

### 方式一：使用环境变量

```powershell
# Windows PowerShell - 使用 DeepSeek
$env:OPENAI_API_KEY="your-deepseek-api-key"
$env:OPENAI_BASE_URL="https://api.deepseek.com/v1"
$env:OPENAI_MODEL="deepseek-chat"

go run main.go
```

```bash
# Linux/Mac - 使用 DeepSeek
export OPENAI_API_KEY="your-deepseek-api-key"
export OPENAI_BASE_URL="https://api.deepseek.com/v1"
export OPENAI_MODEL="deepseek-chat"

go run main.go
```

### 方式二：使用命令行参数

```bash
# 使用预设提供者
go run main.go -provider=deepseek -key=your-api-key

# 使用 OpenAI
go run main.go -provider=openai -key=your-api-key

# 使用本地 Ollama
go run main.go -provider=ollama -key=ollama

# 自定义 URL 和模型
go run main.go -url=https://api.deepseek.com/v1 -model=deepseek-chat -key=your-api-key
```

### 示例对话

```
=================================
     Go AI Agent 学习框架
=================================

📡 API: https://api.deepseek.com/v1
🤖 Model: deepseek-chat

已加载工具:
  - calculator   : 数学计算
  - system_info  : 系统信息
  - shell        : Shell 命令执行

输入 'quit' 或 'exit' 退出
=================================

👤 You: 帮我算一下 123 * 456
🤖 Agent: 计算结果: 123 * 456 = 56088

👤 You: 查看当前目录
🤖 Agent: [调用 shell 工具]
total 24
drwxr-xr-x  5 user user 4096 ...
-rw-r--r--  1 user user 1234 main.go
```

## 添加自定义工具

### 方式一：实现 Tool 接口

```go
package main

import "lite-agent/agent"

// MyTool 自定义工具
type MyTool struct{}

func (t *MyTool) Name() string {
    return "my_tool"
}

func (t *MyTool) Description() string {
    return "这是一个自定义工具"
}

func (t *MyTool) Parameters() map[string]interface{} {
    return map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "input": map[string]interface{}{
                "type":        "string",
                "description": "输入参数",
            },
        },
        "required": []string{"input"},
    }
}

func (t *MyTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
    input := args["input"].(string)
    return "处理结果: " + input, nil
}

// 注册工具
func main() {
    // ...
    ag.AddTool(&MyTool{})
    // ...
}
```

### 方式二：使用闭包（简化版）

```go
// 创建一个简单的工具
ag.AddTool(NewFuncTool(
    "echo",
    "回显输入内容",
    map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "message": map[string]interface{}{"type": "string"},
        },
        "required": []string{"message"},
    },
    func(ctx context.Context, args map[string]interface{}) (string, error) {
        return args["message"].(string), nil
    },
))
```

## 内置工具

### 1. Shell 工具 (`shell`)

执行系统命令，支持安全白名单。

```go
// 安全模式（默认）- 只允许白名单命令
ag.AddTool(tools.NewShellTool())

// 不限制模式（慎用！）- 允许所有命令
ag.AddTool(tools.NewShellToolUnsafe())

// 动态添加允许的命令
shellTool := tools.NewShellTool()
shellTool.AddAllowedCommand("rm")
ag.AddTool(shellTool)
```

默认允许的命令：`ls`, `dir`, `pwd`, `echo`, `cat`, `whoami`, `date`, `ping`, `curl`, `git`, `go`, `npm`, `node`, `python`, `docker`

### 2. 计算器工具 (`calculator`)

执行数学计算表达式。

### 3. 系统信息工具 (`system_info`)

获取当前系统信息。

### 4. 时间工具 (`current_time`)

获取当前日期和时间。

## 核心学习点

1. **工具调用流程**：LLM 如何决定调用哪个工具，如何传递参数
2. **多轮对话**：Agent 如何保持上下文记忆
3. **循环执行**：工具执行结果如何反馈给 LLM 继续推理
4. **错误处理**：工具执行失败时的处理策略

## 扩展方向

- [ ] 添加向量数据库支持（RAG）
- [ ] 添加流式输出支持
- [ ] 添加多 Agent 协作
- [ ] 添加 Web 搜索工具
- [ ] 添加代码执行沙箱
- [ ] 添加对话持久化