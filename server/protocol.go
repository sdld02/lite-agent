package server

import (
	"encoding/json"

	"lite-agent/tools"
)

// ============================================================================
// WebSocket 消息协议定义
// 采用 JSON 格式，按 type 字段区分消息类型
// ============================================================================

// ========== 客户端 → 服务端 ==========

// ClientMessage 客户端发来的消息
type ClientMessage struct {
	Type           string              `json:"type"`                    // 消息类型
	Content        string              `json:"content,omitempty"`       // chat 类型的用户输入
	SessionID      string              `json:"session_id,omitempty"`    // 目标会话 ID
	LLMConfig      *LLMConfigInfo      `json:"llm_config,omitempty"`    // set_llm_config 时的 LLM 配置
	TelegramConfig *TelegramConfigInfo `json:"telegram_config,omitempty"` // set_telegram_config 时的 Telegram 配置
	Answers        map[string]string   `json:"answers,omitempty"`       // answer_question 时的用户答案
	MCPConfig      *MCPConfigInfo      `json:"mcp_config,omitempty"`    // set_mcp_config 时的 MCP 配置
}

// 支持的客户端消息类型常量
const (
	MsgTypeChat          = "chat"           // 发送对话消息
	MsgTypeNewSession    = "new_session"    // 创建新会话
	MsgTypeLoadSession   = "load_session"   // 加载历史会话
	MsgTypeListSessions  = "list_sessions"  // 列出所有会话
	MsgTypeDeleteSession = "delete_session" // 删除会话
	MsgTypeGetTasks      = "get_tasks"      // 获取当前会话的任务列表
	MsgTypeGetStatus     = "get_status"     // 获取服务状态
	MsgTypeCancel        = "cancel"         // 取消指定 session 的执行
	MsgTypeGetLLMConfig  = "get_llm_config" // 获取 LLM 配置
	MsgTypeSetLLMConfig  = "set_llm_config" // 设置 LLM 配置

	// === Telegram Bot 管理 ===
	MsgTypeGetTelegramConfig  = "get_telegram_config"  // 获取 Telegram Bot 配置状态
	MsgTypeSetTelegramConfig  = "set_telegram_config"  // 设置 Telegram Bot Token
	MsgTypeStartTelegramBot   = "start_telegram_bot"   // 启动 Telegram Bot
	MsgTypeStopTelegramBot    = "stop_telegram_bot"    // 停止 Telegram Bot

	// === 用户提问交互 ===
	MsgTypeAnswerQuestion = "answer_question" // 用户回答了提问

	// === MCP 服务器配置管理 ===
	MsgTypeGetMCPConfig = "get_mcp_config" // 获取 MCP 服务器配置列表
	MsgTypeSetMCPConfig = "set_mcp_config" // 保存 MCP 服务器配置列表
)

// ========== 服务端 → 客户端 ==========

// ServerMessage 服务端发出的消息
type ServerMessage struct {
	Type             string            `json:"type"`
	SessionID        string            `json:"session_id,omitempty"`        // 消息所属的 session ID（用于多 session 路由）
	ClientSessionID  string            `json:"client_session_id,omitempty"` // 客户端 session ID（session_info 回传，用于精确关联）
	Content          string            `json:"content,omitempty"`           // 文本内容
	ToolCall         *ToolCallMsg      `json:"tool_call,omitempty"`         // 工具调用信息
	ToolCallProgress *ToolCallProgressMsg `json:"tool_call_progress,omitempty"` // 工具调用参数生成进度
	Result           string            `json:"result,omitempty"`            // 工具执行结果（精简文本，给 LLM 用）
	ToolResultData   interface{}       `json:"tool_result_data,omitempty"`  // 工具富数据（完整结构体，给 UI 用）
	Error            string            `json:"error,omitempty"`             // 错误信息
	Response         string            `json:"response,omitempty"`          // 完整响应（done 时）
	Session          *SessionInfo      `json:"session,omitempty"`           // 会话信息
	Sessions         []SessionInfo     `json:"sessions,omitempty"`          // 会话列表
	Status           *StatusInfo       `json:"status,omitempty"`            // 服务状态
	Messages         []json.RawMessage `json:"messages,omitempty"`          // 历史消息列表（session_loaded 时）
	Tasks            []TaskInfo        `json:"tasks,omitempty"`             // 任务列表（tasks 消息）
	LLMConfig        *LLMConfigInfo      `json:"llm_config,omitempty"`        // LLM 配置（llm_config 消息）
	TelegramConfig   *TelegramConfigInfo `json:"telegram_config,omitempty"`   // Telegram Bot 配置（telegram_config 消息）
	Questions        []tools.Question    `json:"questions,omitempty"`         // 用户问题列表（ask_question 时）
	MCPConfig        *MCPConfigInfo      `json:"mcp_config,omitempty"`        // MCP 服务器配置（mcp_config 消息）
}

// 支持的服务端消息类型常量
const (
	MsgTypeConnected        = "connected"           // 连接建立成功
	MsgTypeContent          = "content"             // 流式正文片段
	MsgTypeReasoning        = "reasoning"           // 流式推理片段
	MsgTypeToolCallProgress = "tool_call_progress"  // 工具调用参数生成进度
	MsgTypeToolCall         = "tool_call"           // 工具调用开始
	MsgTypeToolResult       = "tool_result"         // 工具调用结果
	MsgTypeDone             = "done"                // 本轮对话完成
	MsgTypeError            = "error"               // 错误
	MsgTypeSessionInfo      = "session_info"        // 当前会话信息
	MsgTypeSessionLoaded    = "session_loaded"      // 会话加载完成（含完整历史消息）
	MsgTypeSessionList      = "session_list"        // 会话列表
	MsgTypeTasks            = "tasks"               // 当前会话的任务列表
	MsgTypeStatus           = "status"              // 服务状态
	MsgTypeLLMConfig        = "llm_config"          // LLM 配置信息
	MsgTypeTelegramConfig   = "telegram_config"     // Telegram Bot 配置信息
	MsgTypeAskQuestion      = "ask_question"        // 向用户提问（需要用户交互回答）
	MsgTypeMCPConfig        = "mcp_config"          // MCP 服务器配置信息
)

// ToolCallMsg 工具调用消息
type ToolCallMsg struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// ToolCallProgressMsg 工具调用参数生成进度消息
type ToolCallProgressMsg struct {
	Name      string `json:"name"`
	ArgsBytes int    `json:"args_bytes"`
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

// TaskInfo 任务摘要（发送给客户端）
type TaskInfo struct {
	ID        string   `json:"id"`
	Subject   string   `json:"subject"`
	Status    string   `json:"status"`
	Owner     string   `json:"owner,omitempty"`
	BlockedBy []string `json:"blockedBy,omitempty"`
}

// LLMConfigInfo LLM 配置信息（发送给客户端 / 接收客户端更新）
type LLMConfigInfo struct {
	Provider string `json:"provider"`  // 预设提供者名称（openai/deepseek/moonshot/zhipu/qwen/ollama/custom）
	APIKey   string `json:"api_key"`   // API Key（已脱敏显示）
	BaseURL  string `json:"base_url"`  // API Base URL
	Model    string `json:"model"`     // 模型名称
}

// TelegramConfigInfo Telegram Bot 配置信息（发送给客户端 / 接收客户端更新）
type TelegramConfigInfo struct {
	Token    string `json:"token"`    // Bot Token（脱敏显示：前4后4位）
	Status   string `json:"status"`   // 运行状态: "stopped", "running", "error"
	Username string `json:"username"` // Bot 用户名（运行时才有）
	Error    string `json:"error"`    // 错误信息
}

// MCPServerConfigInfo MCP 服务器配置信息（单个服务器）
type MCPServerConfigInfo struct {
	Name    string            `json:"name"`           // 服务器名称
	Command string            `json:"command"`        // 启动命令
	Args    []string          `json:"args,omitempty"` // 命令参数
	Env     map[string]string `json:"env,omitempty"`  // 环境变量
}

// MCPConfigInfo MCP 服务器配置列表（发送给客户端 / 接收客户端更新）
type MCPConfigInfo struct {
	Servers []MCPServerConfigInfo `json:"servers"` // 服务器列表
}

// ========== 辅助函数 ==========

// NewSessionInfo 从 session.Meta 构建 SessionInfo
func NewSessionInfo(id, createdAt, updatedAt, preview string, messageCount int) SessionInfo {
	return SessionInfo{
		ID:           id,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		MessageCount: messageCount,
		Preview:      preview,
	}
}
