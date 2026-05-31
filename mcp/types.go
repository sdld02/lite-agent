// Package mcp 实现 MCP (Model Context Protocol) 客户端，
// 将外部 MCP 服务器的工具集成到 AI Agent 中。
//
// 设计理念：
//   - 按需加载：配置解析时不启动服务器，首次调用时才建立连接（与 LSP 惰性启动一致）
//   - 单一入口：与 skill 系统一致，提供统一的 "mcp" 工具，通过参数路由到不同服务器
//   - 全局管理器：参考 LSP 的 globalManager 单例模式
//   - 纯标准库：JSON-RPC 2.0 使用 encoding/json 实现，无需外部依赖
//
// MCP 协议参考：https://spec.modelcontextprotocol.io/specification/2024-11-05/
package mcp

import (
	"encoding/json"
	"strconv"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 基础类型
// ---------------------------------------------------------------------------

// jsonrpcRequest JSON-RPC 2.0 请求
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcNotification JSON-RPC 2.0 通知（无 id）
type jsonrpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse JSON-RPC 2.0 响应
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError JSON-RPC 2.0 错误
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error 实现 error 接口
func (e *jsonrpcError) Error() string {
	return "MCP error [" + strconv.Itoa(e.Code) + "]: " + e.Message
}

// ---------------------------------------------------------------------------
// MCP 方法常量
// ---------------------------------------------------------------------------

const (
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized"
	MethodToolsList   = "tools/list"
	MethodToolsCall   = "tools/call"
)

// ---------------------------------------------------------------------------
// MCP 初始化类型
// ---------------------------------------------------------------------------

// InitializeParams MCP initialize 请求参数
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientCapabilities 客户端能力声明
type ClientCapabilities struct {
	// 可以按需扩展，目前为空对象即可
}

// ClientInfo 客户端信息
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult MCP initialize 响应结果
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities 服务器能力声明
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability 工具能力
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo 服务器信息
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ---------------------------------------------------------------------------
// MCP 工具类型
// ---------------------------------------------------------------------------

// ListToolsResult tools/list 的响应结果
type ListToolsResult struct {
	Tools      []MCPToolDefinition `json:"tools"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// MCPToolDefinition MCP 工具定义（来自服务器的 tools/list 响应）
type MCPToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// CallToolParams tools/call 的请求参数
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// CallToolResult tools/call 的响应结果
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ContentItem MCP 内容项（text/image/resource 等多种类型）
// content 是一个异构数组，通过 type 字段区分
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`     // type="text"
	Data     string `json:"data,omitempty"`     // type="image" | "resource" (base64)
	MimeType string `json:"mimeType,omitempty"` // type="image" | "resource"
	URI      string `json:"uri,omitempty"`      // type="resource"
}

// String 将内容项转换为纯文本（供 LLM 读取）
func (c *ContentItem) String() string {
	switch c.Type {
	case "text":
		return c.Text
	case "image":
		return "[Image: " + c.MimeType + ", " + formatBytes(len(c.Data)) + "]"
	case "resource":
		if c.Text != "" {
			return c.Text
		}
		return "[Resource: " + c.URI + "]"
	default:
		return "[Unknown content type: " + c.Type + "]"
	}
}

// ContentText 提取结果中所有文本内容的拼接
func (r *CallToolResult) ContentText() string {
	if len(r.Content) == 0 {
		return "(empty result)"
	}
	var result string
	for i, item := range r.Content {
		if i > 0 {
			result += "\n"
		}
		result += item.String()
	}
	return result
}

// ---------------------------------------------------------------------------
// 服务器配置类型
// ---------------------------------------------------------------------------

// ServerConfig 单个 MCP 服务器的配置
type ServerConfig struct {
	Name     string            `json:"name"`
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"` // true=禁用该服务器（默认启用）
}

// MCPConfig mcp.json 文件顶层结构
type MCPConfig struct {
	Servers []ServerConfig `json:"servers"`
}

// ServerDescription 服务器描述信息（用于系统提示词）
type ServerDescription struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Description string `json:"description"` // 来自 initialize 响应的 serverInfo
}

func formatBytes(n int) string {
	if n < 1024 {
		return strconv.Itoa(n) + "B"
	}
	if n < 1024*1024 {
		return strconv.Itoa(n/1024) + "KB"
	}
	return strconv.Itoa(n/(1024*1024)) + "MB"
}
