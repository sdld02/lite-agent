package tools

import (
	"context"
	"fmt"
	"strings"

	"lite-agent/agent"
)

// ============================================================================
// QuestionHandler — 上下文注入的回调，由 handler/CLI 提供具体实现
// ============================================================================

// QuestionHandler 问题处理器类型
// 传入问题列表，阻塞直到用户回答，返回 question -> answer 的映射
type QuestionHandler func(questions []Question) (map[string]string, error)

type contextKey string

const askUserQuestionKey contextKey = "ask_user_question_handler"

// SetQuestionHandler 将 QuestionHandler 注入 context
func SetQuestionHandler(ctx context.Context, handler QuestionHandler) context.Context {
	return context.WithValue(ctx, askUserQuestionKey, handler)
}

// getQuestionHandler 从 context 中取出 QuestionHandler
func getQuestionHandler(ctx context.Context) (QuestionHandler, bool) {
	h, ok := ctx.Value(askUserQuestionKey).(QuestionHandler)
	return h, ok
}

// ============================================================================
// 数据模型
// ============================================================================

// Question 问题和选项定义
type Question struct {
	Question    string   `json:"question"`     // 完整问题文本，以问号结尾
	Header      string   `json:"header"`       // 短标签（≤12字符），如 "Auth method"
	Options     []Option `json:"options"`      // 选项列表（2-4个）
	MultiSelect bool     `json:"multi_select"` // 是否允许多选
}

// Option 选项定义
type Option struct {
	Label       string `json:"label"`       // 选项文本（1-5词）
	Description string `json:"description"` // 选项说明
}

// AskUserQuestionTool 向用户提问工具
type AskUserQuestionTool struct{}

// NewAskUserQuestionTool 创建提问工具实例
func NewAskUserQuestionTool() *AskUserQuestionTool {
	return &AskUserQuestionTool{}
}

func (t *AskUserQuestionTool) Name() string {
	return "ask_user_question"
}

func (t *AskUserQuestionTool) Description() string {
	return "在任务执行过程中向用户提问（多选题），用于收集偏好、澄清歧义、获取决策。提问会弹出UI界面等待用户回答。"
}

func (t *AskUserQuestionTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type":        "array",
				"description": "要问用户的问题列表（1-4个问题）。用户总会看到一个\"Other\"选项用于输入自定义答案。",
				"minItems":    1,
				"maxItems":    4,
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"question": map[string]interface{}{
							"type":        "string",
							"description": "完整问题文本，应清晰、具体并以问号结尾。如 '应该使用哪个日期格式化库？' 如果是多选，相应措辞如 '要启用哪些功能？'",
						},
						"header": map[string]interface{}{
							"type":        "string",
							"description": "非常简短的标签（最多12个字符），如 '认证方式'、'库'、'方案'",
						},
						"options": map[string]interface{}{
							"type":        "array",
							"description": "该问题的可选选项。必须有2-4个选项。每个选项应该是互斥的选择（多选除外）。不要添加'其他'选项，系统会自动提供。",
							"minItems":    2,
							"maxItems":    4,
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"label": map[string]interface{}{
										"type":        "string",
										"description": "该选项的显示文本，用户将看到并选择。应简洁（1-5个词），清楚地描述该选择。",
									},
									"description": map[string]interface{}{
										"type":        "string",
										"description": "解释此选项的含义或选择后的结果。用于提供权衡或影响的上下文。",
									},
								},
								"required": []string{"label", "description"},
							},
						},
						"multi_select": map[string]interface{}{
							"type":        "boolean",
							"description": "设置为 true 允许用户选择多个选项而非仅一个。当选项不互斥时使用。",
							"default":     false,
						},
					},
					"required": []string{"question", "header", "options"},
				},
			},
			"intent": map[string]interface{}{
				"type":        "string",
				"description": "调用此工具的意图，如：询问用户偏好的认证方式",
			},
		},
		"required": []string{"questions", "intent"},
	}
}

func (t *AskUserQuestionTool) Execute(ctx context.Context, args map[string]interface{}) (*agent.ToolResult, error) {
	// 1. 解析输入
	questionsRaw, ok := args["questions"].([]interface{})
	if !ok {
		return &agent.ToolResult{
			Content: agent.FormatValidationError("questions 参数必须是数组"),
			IsError: true,
		}, nil
	}

	if len(questionsRaw) < 1 || len(questionsRaw) > 4 {
		return &agent.ToolResult{
			Content: agent.FormatValidationError(fmt.Sprintf("问题数量必须在1-4之间，当前: %d", len(questionsRaw))),
			IsError: true,
		}, nil
	}

	questions := make([]Question, 0, len(questionsRaw))
	for i, qr := range questionsRaw {
		qm, ok := qr.(map[string]interface{})
		if !ok {
			return &agent.ToolResult{
				Content: agent.FormatValidationError(fmt.Sprintf("第%d个问题格式无效", i+1)),
				IsError: true,
			}, nil
		}

		q := Question{
			Question: getString(qm, "question"),
			Header:   getString(qm, "header"),
		}

		// 校验 header 长度
		if len([]rune(q.Header)) > 12 {
			q.Header = string([]rune(q.Header)[:12])
		}

		// 解析 multi_select
		if ms, ok := qm["multi_select"].(bool); ok {
			q.MultiSelect = ms
		}

		// 解析 options
		optsRaw, ok := qm["options"].([]interface{})
		if !ok || len(optsRaw) < 2 || len(optsRaw) > 4 {
			return &agent.ToolResult{
				Content: agent.FormatValidationError(fmt.Sprintf("第%d个问题的选项数必须在2-4之间", i+1)),
				IsError: true,
			}, nil
		}

		for _, or := range optsRaw {
			om, ok := or.(map[string]interface{})
			if !ok {
				continue
			}
			q.Options = append(q.Options, Option{
				Label:       getString(om, "label"),
				Description: getString(om, "description"),
			})
		}

		questions = append(questions, q)
	}

	// 2. 检查是否有 QuestionHandler（由 handler/CLI 在上下文中注入）
	handler, hasHandler := getQuestionHandler(ctx)
	if !hasHandler {
		// 没有注入处理器时，直接返回提示信息（静默降级）
		return &agent.ToolResult{
			Content: fmt.Sprintf("[ask_user_question 降级] 无法向用户提问（当前环境不支持交互式提问）。\n问题列表：%s", formatQuestionsFallback(questions)),
		}, nil
	}

	// 3. 调用处理器（阻塞直到用户回答）
	answers, err := handler(questions)
	if err != nil {
		return &agent.ToolResult{
			Content: agent.FormatToolError(fmt.Errorf("提问失败: %w", err)),
			IsError: true,
		}, nil
	}

	// 4. 格式化返回结果
	result := formatAnswers(questions, answers)
	return &agent.ToolResult{
		Content:  result,
		RichData: map[string]interface{}{"questions": questions, "answers": answers},
	}, nil
}

// getString 安全获取字符串字段
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// formatQuestionsFallback 降级模式下格式化问题列表
func formatQuestionsFallback(questions []Question) string {
	var sb strings.Builder
	for i, q := range questions {
		fmt.Fprintf(&sb, "\n%d. [%s] %s\n", i+1, q.Header, q.Question)
		for j, opt := range q.Options {
			fmt.Fprintf(&sb, "   %c) %s — %s\n", 'A'+j, opt.Label, opt.Description)
		}
		if q.MultiSelect {
			sb.WriteString("   (可多选)\n")
		}
	}
	return sb.String()
}

// formatAnswers 格式化用户答案返回给 LLM
func formatAnswers(questions []Question, answers map[string]string) string {
	var sb strings.Builder
	sb.WriteString("用户已回答了以下问题：\n")
	for _, q := range questions {
		answer, ok := answers[q.Question]
		if !ok || answer == "" {
			answer = "(未回答)"
		}
		fmt.Fprintf(&sb, `- "%s" → "%s"`+"\n", q.Question, answer)
	}
	sb.WriteString("\n请根据用户的回答继续执行任务。")
	return sb.String()
}
