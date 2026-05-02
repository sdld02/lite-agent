package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolResult 工具执行结果（分离 LLM 文本与 UI 数据）
type ToolResult struct {
	Content  string      // 返回给 LLM 的精简文本
	RichData interface{} // 可选：完整输出结构体，供 UI/WebSocket 使用（nil 表示无富数据）
	IsError  bool        // 是否为错误结果
}

// FormatToolError 格式化工具执行错误（供 LLM 识别）
func FormatToolError(err error) string {
	return "<tool_use_error>" + err.Error() + "</tool_use_error>"
}

// FormatValidationError 格式化输入验证错误
func FormatValidationError(msg string) string {
	return "<tool_use_error>InputValidationError: " + msg + "</tool_use_error>"
}

// Tool 定义 Agent 工具接口
type Tool interface {
	// Name 工具名称
	Name() string
	// Description 工具描述
	Description() string
	// Parameters 工具参数定义 (JSON Schema)
	Parameters() map[string]interface{}
	// Execute 执行工具，返回 ToolResult（Content 给 LLM，RichData 给 UI）
	Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error)
}

// Message 消息结构
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // 推理模型的思考过程
	// 工具调用信息
	ToolCallID   string                 `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall             `json:"tool_calls,omitempty"`
	ToolResponse map[string]interface{} `json:"tool_response,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数调用
type FunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// LLMProvider LLM 提供者接口
type LLMProvider interface {
	// Chat 发送消息并获取响应
	Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Message, error)
}

// StreamCallback 流式输出回调，每收到一个文本片段时调用
type StreamCallback func(chunk string)

// StreamFlushCallback 流式渲染刷新回调，在 tool call 执行前调用
// content 为当前轮次累积的文本内容，调用方可据此做清屏+渲染
type StreamFlushCallback func(content string)

// StreamProvider 支持流式输出的 LLM 提供者接口
type StreamProvider interface {
	LLMProvider
	// ChatStream 流式发送消息，通过回调实时返回文本片段，最终返回完整的 Message（含 tool_calls）
	ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, callback StreamCallback) (*Message, error)
}

// ReasoningCallback 推理内容流式回调，每收到一个推理片段时调用
type ReasoningCallback func(chunk string)

// ReasoningStreamProvider 支持推理输出的流式 LLM 提供者接口
type ReasoningStreamProvider interface {
	StreamProvider
	// ChatStreamReasoning 流式发送消息，通过双回调分别返回推理和正文片段
	ChatStreamReasoning(ctx context.Context, messages []Message, tools []ToolDefinition,
		onContent StreamCallback, onReasoning ReasoningCallback) (*Message, error)
}

// ToolDefinition 工具定义（用于发送给 LLM）
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition 函数定义
type FunctionDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCallObserver 工具调用观察者回调
// 在工具执行前后调用，用于通知外部（如 WebSocket 推送）
// result == nil 表示工具调用开始，result != nil 表示工具调用结束
type ToolCallObserver func(toolName string, args map[string]interface{}, result *ToolResult)

// Agent AI Agent 结构
type Agent struct {
	provider     LLMProvider
	tools        map[string]Tool
	memory       []Message
	systemPrompt string // 系统提示词
	maxSteps     int    // 最大执行步数，防止无限循环
	toolObserver ToolCallObserver // 可选：工具调用观察者（默认 nil 时使用 fmt.Printf 输出）
}

// NewAgent 创建新的 Agent
func NewAgent(provider LLMProvider) *Agent {
	return &Agent{
		provider: provider,
		tools:    make(map[string]Tool),
		memory:   make([]Message, 0),
		maxSteps: 50,
	}
}

// AddTool 添加工具
func (a *Agent) AddTool(tool Tool) {
	a.tools[tool.Name()] = tool
}

// SetSystemPrompt 设置系统提示词
func (a *Agent) SetSystemPrompt(prompt string) {
	a.systemPrompt = prompt
}

// SetMaxSteps 设置最大执行步数
func (a *Agent) SetMaxSteps(steps int) {
	a.maxSteps = steps
}

// SetToolObserver 设置工具调用观察者
// 设置后，工具调用信息会通过回调通知（而非 fmt.Printf 输出）
// 设置为 nil 可恢复默认的 fmt.Printf 输出行为
func (a *Agent) SetToolObserver(observer ToolCallObserver) {
	a.toolObserver = observer
}

// GetMemory 返回当前 memory 的副本
func (a *Agent) GetMemory() []Message {
	copied := make([]Message, len(a.memory))
	copy(copied, a.memory)
	return copied
}

// SetMemory 恢复历史 memory（用于加载持久化的会话）
func (a *Agent) SetMemory(messages []Message) {
	a.memory = messages
}

// Run 运行 Agent
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// 先将用户消息加入 memory（确保持久化时包含用户消息）
	if userInput != "" {
		a.memory = append(a.memory, Message{Role: "user", Content: userInput})
	}
	// 构建消息列表（包含系统提示词）
	messages := a.buildMessages("")

	// 循环执行
	for i := 0; i < a.maxSteps; i++ {
		// 获取 LLM 响应
		response, err := a.provider.Chat(ctx, messages, a.getToolDefinitions())
		if err != nil {
			return "", fmt.Errorf("LLM 调用失败: %w", err)
		}

		// 添加助手消息到记忆
		a.memory = append(a.memory, *response)
		messages = append(messages, *response)

		// 检查是否需要调用工具
		if len(response.ToolCalls) > 0 {
			// 执行工具调用
			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls)
			if err != nil {
				return "", err
			}

			// 添加工具结果到记忆
			a.memory = append(a.memory, toolResults...)
			messages = append(messages, toolResults...)
			continue
		}

		// 没有工具调用，返回最终结果
		return response.Content, nil
	}

	return "", fmt.Errorf("达到最大执行步数 %d，可能存在循环", a.maxSteps)
}

// getToolDefinitions 获取工具定义列表
func (a *Agent) getToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(a.tools))
	for _, tool := range a.tools {
		definitions = append(definitions, ToolDefinition{
			Type: "function",
			Function: FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			},
		})
	}
	return definitions
}

// executeToolCalls 执行工具调用
func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []ToolCall) ([]Message, error) {
	var results []Message

	for _, tc := range toolCalls {
		tool, exists := a.tools[tc.Function.Name]
		if !exists {
			// 未知工具返回错误结果而非中断整个流程
			errResult := &ToolResult{
				Content: FormatToolError(fmt.Errorf("未知工具: %s", tc.Function.Name)),
				IsError: true,
			}
			if a.toolObserver != nil {
				a.toolObserver(tc.Function.Name, nil, errResult)
			}
			results = append(results, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    errResult.Content,
			})
			continue
		}

		// 解析参数
		var args map[string]interface{}
		if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
			errResult := &ToolResult{
				Content: FormatValidationError(fmt.Sprintf("解析参数失败: %v", err)),
				IsError: true,
			}
			if a.toolObserver != nil {
				a.toolObserver(tc.Function.Name, nil, errResult)
			}
			results = append(results, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    errResult.Content,
			})
			continue
		}

		fmt.Println(a.toolObserver)
		// 通知：工具调用开始
		if a.toolObserver != nil {
			a.toolObserver(tc.Function.Name, args, nil)
		} else {
			fmt.Printf("\n🔧 调用工具: %s\n", tc.Function.Name)
			if "shell" == tc.Function.Name {
				fmt.Printf("   意图: %s\n", args["intent"])
				fmt.Printf("   命令: %s\n", args["command"])
			} else {
				fmt.Printf("   参数: %s\n", string(tc.Function.Arguments))
			}
		}

		// 执行工具
		toolResult, err := tool.Execute(ctx, args)
		if err != nil {
			toolResult = &ToolResult{
				Content: FormatToolError(err),
				IsError: true,
			}
		}

		// 通知：工具执行完毕
		if a.toolObserver != nil {
			a.toolObserver(tc.Function.Name, args, toolResult)
		} else {
			fmt.Printf("   结果: %s\n\n", toolResult.Content)
		}

		// 添加工具结果消息
		results = append(results, Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    toolResult.Content,
		})
	}

	return results, nil
}

// RunStream 以流式模式运行 Agent，实时输出文本片段
// onChunk: 每收到一个正文片段时调用
// onReasoning: 每收到一个推理片段时调用（推理模型专用，非推理模型可传 nil）
// onFlush: 在执行 tool call 前调用，传入当前轮次累积的文本，供调用方清屏+渲染
func (a *Agent) RunStream(ctx context.Context, userInput string, onChunk StreamCallback, onReasoning ReasoningCallback, onFlush StreamFlushCallback) (string, error) {
	// 优先尝试 ReasoningStreamProvider（支持推理输出的流式接口）
	rsp, isReasoning := a.provider.(ReasoningStreamProvider)
	if !isReasoning {
		// 回退到普通 StreamProvider
		if _, ok := a.provider.(StreamProvider); !ok {
			return "", fmt.Errorf("当前 LLM 提供者不支持流式输出")
		}
	}

	// 先将用户消息加入 memory（确保持久化时包含用户消息）
	if userInput != "" {
		a.memory = append(a.memory, Message{Role: "user", Content: userInput})
	}
	messages := a.buildMessages("")

	for i := 0; i < a.maxSteps; i++ {
		var response *Message
		var err error

		if isReasoning && onReasoning != nil {
			response, err = rsp.ChatStreamReasoning(ctx, messages, a.getToolDefinitions(), onChunk, onReasoning)
		} else {
			sp := a.provider.(StreamProvider)
			response, err = sp.ChatStream(ctx, messages, a.getToolDefinitions(), onChunk)
		}
		if err != nil {
			return "", fmt.Errorf("LLM 流式调用失败: %w", err)
		}

		a.memory = append(a.memory, *response)
		messages = append(messages, *response)

		if len(response.ToolCalls) > 0 {
			// 先执行工具调用（打印工具信息），再渲染 LLM 文本
			// 这样用户看到的是：工具调用 → 工具结果 → LLM 回复
			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls)

			// 工具执行完毕后再渲染第1轮 LLM 文本（如"让我查看项目结构"）
			if onFlush != nil {
				onFlush(response.Content)
			}
			if err != nil {
				return "", err
			}

			a.memory = append(a.memory, toolResults...)
			messages = append(messages, toolResults...)
			continue
		}

		return response.Content, nil
	}

	return "", fmt.Errorf("达到最大执行步数 %d，可能存在循环", a.maxSteps)
}

// buildMessages 构建消息列表（包含系统提示词和历史记忆）
func (a *Agent) buildMessages(userInput string) []Message {
	messages := make([]Message, 0)

	// 添加系统提示词
	if a.systemPrompt != "" {
		messages = append(messages, Message{
			Role:    "system",
			Content: a.systemPrompt,
		})
	}

	// 添加历史记忆
	messages = append(messages, a.memory...)

	// 添加当前用户输入
	if userInput != "" {
		messages = append(messages, Message{
			Role:    "user",
			Content: userInput,
		})
	}

	return messages
}
