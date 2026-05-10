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

// ============================================================================
// 统一流事件模型
// ============================================================================

// StreamEventType 流事件类型
type StreamEventType int

const (
	EventContent          StreamEventType = iota // 正文文本片段
	EventReasoning                               // 推理内容片段
	EventToolCallProgress                        // 工具调用参数生成进度（LLM 正在生成参数）
	EventFlush                                   // tool call 前的文本刷新通知
	EventToolCallStart                           // 工具开始执行
	EventToolCallEnd                             // 工具执行完毕
)

// StreamEvent 统一流事件
type StreamEvent struct {
	Type       StreamEventType
	Content    string                 // EventContent/EventReasoning/EventFlush: 文本内容
	ToolName   string                 // EventToolCallProgress/Start/End: 工具名称
	ArgsBytes  int                    // EventToolCallProgress: 已接收参数字节数
	ToolArgs   map[string]interface{} // EventToolCallStart/End: 工具参数
	ToolResult *ToolResult            // EventToolCallEnd: 工具执行结果
}

// StreamEventHandler 统一流事件处理回调
type StreamEventHandler func(event StreamEvent)

// StreamProvider 支持流式输出的 LLM 提供者接口
type StreamProvider interface {
	LLMProvider
	// ChatStream 流式发送消息，所有流式事件通过统一 handler 回调
	ChatStream(ctx context.Context, messages []Message, tools []ToolDefinition, handler StreamEventHandler) (*Message, error)
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

// Agent AI Agent 结构
type Agent struct {
	provider     LLMProvider
	tools        map[string]Tool
	memory       []Message
	systemPrompt string // 系统提示词
	maxSteps     int    // 最大执行步数，防止无限循环
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

// Run 运行 Agent（非流式模式）
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
			// 执行工具调用（handler=nil 使用 fmt.Printf 兜底输出）
			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls, nil)
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

// RunStream 以流式模式运行 Agent，所有事件通过统一 handler 回调
func (a *Agent) RunStream(ctx context.Context, userInput string, handler StreamEventHandler) (string, error) {
	sp, ok := a.provider.(StreamProvider)
	if !ok {
		return "", fmt.Errorf("当前 LLM 提供者不支持流式输出")
	}

	// 先将用户消息加入 memory（确保持久化时包含用户消息）
	if userInput != "" {
		a.memory = append(a.memory, Message{Role: "user", Content: userInput})
	}
	messages := a.buildMessages("")

	for i := 0; i < a.maxSteps; i++ {
		response, err := sp.ChatStream(ctx, messages, a.getToolDefinitions(), handler)
		if err != nil {
			return "", fmt.Errorf("LLM 流式调用失败: %w", err)
		}

		a.memory = append(a.memory, *response)
		messages = append(messages, *response)

		if len(response.ToolCalls) > 0 {
			// 先通知 flush（渲染 LLM 文本），再执行工具
			if handler != nil {
				handler(StreamEvent{Type: EventFlush, Content: response.Content})
			}

			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls, handler)
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
// handler 非 nil 时通过事件通知；为 nil 时 fallback 到 fmt.Printf 输出
func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []ToolCall, handler StreamEventHandler) ([]Message, error) {
	var results []Message

	for _, tc := range toolCalls {
		tool, exists := a.tools[tc.Function.Name]
		if !exists {
			// 未知工具返回错误结果而非中断整个流程
			errResult := &ToolResult{
				Content: FormatToolError(fmt.Errorf("未知工具: %s", tc.Function.Name)),
				IsError: true,
			}
			if handler != nil {
				handler(StreamEvent{Type: EventToolCallEnd, ToolName: tc.Function.Name, ToolResult: errResult})
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
			if handler != nil {
				handler(StreamEvent{Type: EventToolCallEnd, ToolName: tc.Function.Name, ToolResult: errResult})
			}
			results = append(results, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    errResult.Content,
			})
			continue
		}

		// 通知：工具调用开始
		if handler != nil {
			handler(StreamEvent{Type: EventToolCallStart, ToolName: tc.Function.Name, ToolArgs: args})
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
		if handler != nil {
			handler(StreamEvent{Type: EventToolCallEnd, ToolName: tc.Function.Name, ToolArgs: args, ToolResult: toolResult})
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
