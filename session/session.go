package session

import (
	"time"

	"lite-agent/agent"
)

// Session 完整会话对象
type Session struct {
	Version      int             `json:"version"`       // 格式版本，当前为 1
	ID           string          `json:"id"`            // 如 "20260424-153000"
	CreatedAt    string          `json:"created_at"`    // RFC3339
	UpdatedAt    string          `json:"updated_at"`    // RFC3339
	Preview      string          `json:"preview"`       // 首条 user 消息前 50 字符
	MessageCount int             `json:"message_count"` // 消息条数
	Messages     []agent.Message `json:"messages"`      // 完整消息历史
}

// Meta 轻量元数据（用于列表展示，不含 Messages）
type Meta struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
}

// NewSession 创建新会话，自动生成 ID 和时间戳
func NewSession() *Session {
	now := time.Now()
	return &Session{
		Version:   1,
		ID:        now.Format("20060102-150405"),
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
}

// Meta 提取轻量元数据
func (s *Session) Meta() Meta {
	return Meta{
		ID:           s.ID,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
		Preview:      s.Preview,
		MessageCount: s.MessageCount,
	}
}

// SetMessages 设置消息列表，自动更新 UpdatedAt、MessageCount、Preview
func (s *Session) SetMessages(msgs []agent.Message) {
	s.Messages = msgs
	s.MessageCount = len(msgs)
	s.UpdatedAt = time.Now().Format(time.RFC3339)

	// 提取首条 user 消息作为预览
	s.Preview = ""
	for _, msg := range msgs {
		if msg.Role == "user" && msg.Content != "" {
			s.Preview = truncate(msg.Content, 50)
			break
		}
	}
}

// truncate 截断字符串到指定 rune 长度
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
