package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"lite-agent/agent"
)

// OpenAIProvider OpenAI API 提供者
type OpenAIProvider struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// OpenAIConfig 配置
type OpenAIConfig struct {
	APIKey  string
	BaseURL string // 可选，用于自定义 API 地址
	Model   string // 默认 gpt-4o
}

// NewOpenAIProvider 创建 OpenAI 提供者
func NewOpenAIProvider(config OpenAIConfig) *OpenAIProvider {
	baseURL := config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	model := config.Model
	if model == "" {
		model = "gpt-4o"
	}

	return &OpenAIProvider{
		apiKey:  config.APIKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// openAIRequest OpenAI API 请求结构
type openAIRequest struct {
	Model      string          `json:"model"`
	Messages   []openAIMessage `json:"messages"`
	Tools      []openAITool    `json:"tools,omitempty"`
	ToolChoice string          `json:"tool_choice,omitempty"`
	Stream     bool            `json:"stream,omitempty"`
}

// openAIMessage OpenAI 消息结构
type openAIMessage struct {
	Role             string           `json:"role"`
	Content          interface{}      `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
}

// openAITool OpenAI 工具定义
type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

// openAIFunctionDef OpenAI 函数定义
type openAIFunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// openAIToolCall OpenAI 工具调用
type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

// openAIFunctionCall OpenAI 函数调用
type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openAIResponse OpenAI 响应结构
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Role             string           `json:"role"`
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openAIStreamResponse 流式响应中的单个 chunk
type openAIStreamResponse struct {
	Choices []struct {
		Delta struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []openAIStreamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// openAIStreamToolCall 流式响应中的 tool_call 片段
type openAIStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// buildChatRequest 构建 chat/completions 请求体（消息转换 + 工具定义 + 序列化）
func (p *OpenAIProvider) buildChatRequest(messages []agent.Message, tools []agent.ToolDefinition, stream bool) ([]byte, error) {
	// 转换消息格式
	openAIMessages := make([]openAIMessage, len(messages))
	for i, msg := range messages {
		openAIMessages[i] = openAIMessage{
			Role:             msg.Role,
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCallID:       msg.ToolCallID,
		}

		// 转换工具调用
		if len(msg.ToolCalls) > 0 {
			openAIMessages[i].ToolCalls = make([]openAIToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				openAIMessages[i].ToolCalls[j] = openAIToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openAIFunctionCall{
						Name:      tc.Function.Name,
						Arguments: string(tc.Function.Arguments),
					},
				}
			}
		}
	}

	// 构建请求
	req := openAIRequest{
		Model:    p.model,
		Messages: openAIMessages,
		Stream:   stream,
	}

	// 添加工具
	if len(tools) > 0 {
		req.Tools = make([]openAITool, len(tools))
		for i, t := range tools {
			req.Tools[i] = openAITool{
				Type: t.Type,
				Function: openAIFunctionDef{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				},
			}
		}
		req.ToolChoice = "auto"
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}
	return body, nil
}

// convertToolCalls 将 openAIToolCall 切片转换为 agent.ToolCall 切片
func convertToolCalls(toolCalls []openAIToolCall) []agent.ToolCall {
	result := make([]agent.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		result[i] = agent.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: agent.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			},
		}
	}
	return result
}

// Chat 实现 LLMProvider 接口
func (p *OpenAIProvider) Chat(ctx context.Context, messages []agent.Message, tools []agent.ToolDefinition) (*agent.Message, error) {
	body, err := p.buildChatRequest(messages, tools, false)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 解析响应
	var openAIResp openAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	if openAIResp.Error.Message != "" {
		return nil, fmt.Errorf("API 错误: %s", openAIResp.Error.Message)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("无响应内容")
	}

	choice := openAIResp.Choices[0]

	// 转换响应
	result := &agent.Message{
		Role:             choice.Message.Role,
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
	}

	// 转换工具调用
	if len(choice.Message.ToolCalls) > 0 {
		result.ToolCalls = convertToolCalls(choice.Message.ToolCalls)
	}

	return result, nil
}

// ChatStream 实现 StreamProvider 接口，流式发送消息
func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []agent.Message, tools []agent.ToolDefinition, callback agent.StreamCallback) (*agent.Message, error) {
	body, err := p.buildChatRequest(messages, tools, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	// 流式请求不使用固定 Timeout 的 client，用独立 client 避免影响非流式调用
	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 返回状态码 %d: %s", resp.StatusCode, string(respBody))
	}

	// 解析 SSE 流
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	// toolCallsMap 用于按 index 累积 tool_call 片段
	toolCallsMap := make(map[int]*openAIToolCall)
	role := "assistant"

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE 格式：以 "data: " 开头
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// 流结束标志
		if data == "[DONE]" {
			break
		}

		var chunk openAIStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Error.Message != "" {
			return nil, fmt.Errorf("API 错误: %s", chunk.Error.Message)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Role != "" {
			role = delta.Role
		}

		// 处理推理内容（静默累积，不通过回调输出）
		if delta.ReasoningContent != "" {
			reasoningBuilder.WriteString(delta.ReasoningContent)
		}

		// 处理文本内容
		if delta.Content != "" {
			contentBuilder.WriteString(delta.Content)
			if callback != nil {
				callback(delta.Content)
			}
		}

		// 处理 tool_calls 片段（按 index 累积）
		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallsMap[tc.Index]
			if !ok {
				existing = &openAIToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openAIFunctionCall{
						Name: tc.Function.Name,
					},
				}
				toolCallsMap[tc.Index] = existing
			} else {
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				if tc.Function.Name != "" {
					existing.Function.Name = tc.Function.Name
				}
			}
			existing.Function.Arguments += tc.Function.Arguments
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取流失败: %w", err)
	}

	// 组装最终 Message
	result := &agent.Message{
		Role:             role,
		Content:          contentBuilder.String(),
		ReasoningContent: reasoningBuilder.String(),
	}

	if len(toolCallsMap) > 0 {
		toolCalls := make([]openAIToolCall, 0, len(toolCallsMap))
		for _, tc := range toolCallsMap {
			toolCalls = append(toolCalls, *tc)
		}
		result.ToolCalls = convertToolCalls(toolCalls)
	}

	return result, nil
}

// ChatStreamReasoning 实现 ReasoningStreamProvider 接口，流式发送消息并分别回调推理和正文内容
func (p *OpenAIProvider) ChatStreamReasoning(ctx context.Context, messages []agent.Message, tools []agent.ToolDefinition, onContent agent.StreamCallback, onReasoning agent.ReasoningCallback) (*agent.Message, error) {
	body, err := p.buildChatRequest(messages, tools, true)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	streamClient := &http.Client{}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 返回状态码 %d: %s", resp.StatusCode, string(respBody))
	}

	// 解析 SSE 流
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCallsMap := make(map[int]*openAIToolCall)
	role := "assistant"

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var chunk openAIStreamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Error.Message != "" {
			return nil, fmt.Errorf("API 错误: %s", chunk.Error.Message)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Role != "" {
			role = delta.Role
		}

		// 处理推理内容
		if delta.ReasoningContent != "" {
			reasoningBuilder.WriteString(delta.ReasoningContent)
			if onReasoning != nil {
				onReasoning(delta.ReasoningContent)
			}
		}

		// 处理正文内容
		if delta.Content != "" {
			contentBuilder.WriteString(delta.Content)
			if onContent != nil {
				onContent(delta.Content)
			}
		}

		// 处理 tool_calls 片段（按 index 累积）
		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallsMap[tc.Index]
			if !ok {
				existing = &openAIToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: openAIFunctionCall{
						Name: tc.Function.Name,
					},
				}
				toolCallsMap[tc.Index] = existing
			} else {
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				if tc.Function.Name != "" {
					existing.Function.Name = tc.Function.Name
				}
			}
			existing.Function.Arguments += tc.Function.Arguments
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取流失败: %w", err)
	}

	// 组装最终 Message
	result := &agent.Message{
		Role:             role,
		Content:          contentBuilder.String(),
		ReasoningContent: reasoningBuilder.String(),
	}

	if len(toolCallsMap) > 0 {
		toolCalls := make([]openAIToolCall, 0, len(toolCallsMap))
		for _, tc := range toolCallsMap {
			toolCalls = append(toolCalls, *tc)
		}
		result.ToolCalls = convertToolCalls(toolCalls)
	}

	return result, nil
}
