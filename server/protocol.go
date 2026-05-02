package server

// ============================================================================
// WebSocket 消息协议定义
// 采用 JSON 格式，按 type 字段区分消息类型
// ============================================================================

// ========== 客户端 → 服务端 ==========

// ClientMessage 客户端发来的消息
type ClientMessage struct {
	Type      string `json:"type"`                 // 消息类型
	Content   string `json:"content,omitempty"`    // chat 类型的用户输入
	SessionID string `json:"session_id,omitempty"` // 目标会话 ID
}

// 支持的客户端消息类型常量
const (
	MsgTypeChat          = "chat"           // 发送对话消息
	MsgTypeNewSession    = "new_session"    // 创建新会话
	MsgTypeLoadSession   = "load_session"   // 加载历史会话
	MsgTypeListSessions  = "list_sessions"  // 列出所有会话
	MsgTypeDeleteSession = "delete_session" // 删除会话
	MsgTypeGetStatus     = "get_status"     // 获取服务状态
)

// ========== 服务端 → 客户端 ==========

// ServerMessage 服务端发出的消息
type ServerMessage struct {
	Type           string        `json:"type"`
	Content        string        `json:"content,omitempty"`          // 文本内容
	ToolCall       *ToolCallMsg  `json:"tool_call,omitempty"`        // 工具调用信息
	Result         string        `json:"result,omitempty"`           // 工具执行结果（精简文本，给 LLM 用）
	ToolResultData interface{}   `json:"tool_result_data,omitempty"` // 工具富数据（完整结构体，给 UI 用）
	Error          string        `json:"error,omitempty"`            // 错误信息
	Response       string        `json:"response,omitempty"`         // 完整响应（done 时）
	Session        *SessionInfo  `json:"session,omitempty"`          // 会话信息
	Sessions       []SessionInfo `json:"sessions,omitempty"`         // 会话列表
	Status         *StatusInfo   `json:"status,omitempty"`           // 服务状态
}

// 支持的服务端消息类型常量
const (
	MsgTypeConnected   = "connected"    // 连接建立成功
	MsgTypeContent     = "content"      // 流式正文片段
	MsgTypeReasoning   = "reasoning"    // 流式推理片段
	MsgTypeToolCall    = "tool_call"    // 工具调用开始
	MsgTypeToolResult  = "tool_result"  // 工具调用结果
	MsgTypeDone        = "done"         // 本轮对话完成
	MsgTypeError       = "error"        // 错误
	MsgTypeSessionInfo = "session_info" // 当前会话信息
	MsgTypeSessionList = "session_list" // 会话列表
	MsgTypeStatus      = "status"       // 服务状态
)

// ToolCallMsg 工具调用消息
type ToolCallMsg struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
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
