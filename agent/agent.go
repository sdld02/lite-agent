package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool 定义 Agent 工具接口
type Tool interface {
	// Name 工具名称
	Name() string
	// Description 工具描述
	Description() string
	// Parameters 工具参数定义 (JSON Schema)
	Parameters() map[string]interface{}
	// Execute 执行工具
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

// Message 消息结构
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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

// Run 运行 Agent
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	// 构建消息列表（包含系统提示词）
	messages := a.buildMessages(userInput)

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
			return nil, fmt.Errorf("未知工具: %s", tc.Function.Name)
		}

		// 解析参数
		var args map[string]interface{}
		if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
			return nil, fmt.Errorf("解析参数失败: %w", err)
		}

		// 打印工具调用信息
		fmt.Printf("\n🔧 调用工具: %s\n", tc.Function.Name)
		if "shell" == tc.Function.Name {
			fmt.Printf("   意图: %s\n", args["intent"])
			fmt.Printf("   命令: %s\n", args["command"])
		}else{
			fmt.Printf("   参数: %s\n", string(tc.Function.Arguments))
		}


		// 执行工具
		result, err := tool.Execute(ctx, args)
		if err != nil {
			result = fmt.Sprintf("错误: %v", err)
		}

		// 打印工具执行结果
		fmt.Printf("   结果: %s\n\n", result)

		// 添加工具结果消息
		results = append(results, Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    result,
		})
	}

	return results, nil
}

// RunStream 以流式模式运行 Agent，实时输出文本片段
// onChunk: 每收到一个文本片段时调用
// onFlush: 在执行 tool call 前调用，传入当前轮次累积的文本，供调用方清屏+渲染
func (a *Agent) RunStream(ctx context.Context, userInput string, onChunk StreamCallback, onFlush StreamFlushCallback) (string, error) {
	sp, ok := a.provider.(StreamProvider)
	if !ok {
		return "", fmt.Errorf("当前 LLM 提供者不支持流式输出")
	}

	messages := a.buildMessages(userInput)

	for i := 0; i < a.maxSteps; i++ {
		response, err := sp.ChatStream(ctx, messages, a.getToolDefinitions(), onChunk)
		if err != nil {
			return "", fmt.Errorf("LLM 流式调用失败: %w", err)
		}

		a.memory = append(a.memory, *response)
		messages = append(messages, *response)

		if len(response.ToolCalls) > 0 {
			// 执行工具前，先通知调用方渲染当前已输出的内容
			if onFlush != nil {
				onFlush(response.Content)
			}

			toolResults, err := a.executeToolCalls(ctx, response.ToolCalls)
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
