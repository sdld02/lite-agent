package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

// CalculatorTool 计算器工具（纯 Go 实现，不依赖外部命令）
type CalculatorTool struct{}

func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{}
}

func (t *CalculatorTool) Name() string {
	return "calculator"
}

func (t *CalculatorTool) Description() string {
	return "执行数学计算表达式，支持加减乘除等基本运算。例如: 2+2, 10*5, 100/4"
}

func (t *CalculatorTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "数学表达式，如: 123+456",
			},
		},
		"required": []string{"expression"},
	}
}

func (t *CalculatorTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	expression, ok := args["expression"].(string)
	if !ok {
		return "", fmt.Errorf("expression 参数必须是字符串")
	}

	result, err := Calculate(expression)
	if err != nil {
		return "", fmt.Errorf("计算失败: %v", err)
	}

	return fmt.Sprintf("计算结果: %s = %v", expression, result), nil
}

// ==================== 纯 Go 表达式求值器 ====================

// 支持的数学函数
var mathFuncs = map[string]func(float64) float64{
	"abs":   math.Abs,
	"sqrt":  math.Sqrt,
	"cbrt":  math.Cbrt,
	"ceil":  math.Ceil,
	"floor": math.Floor,
	"round": math.Round,
	"sin":   math.Sin,
	"cos":   math.Cos,
	"tan":   math.Tan,
	"asin":  math.Asin,
	"acos":  math.Acos,
	"atan":  math.Atan,
	"exp":   math.Exp,
	"log":   math.Log,
	"log10": math.Log10,
	"log2":  math.Log2,
}

// token 类型
type tokenType int

const (
	tokNumber  tokenType = iota
	tokPlus
	tokMinus
	tokMul
	tokDiv
	tokLParen
	tokRParen
	tokFunc
	tokComma
	tokEOF
)

type token struct {
	typ   tokenType
	value string
	num   float64
}

// lexer 词法分析器
type lexer struct {
	input string
	pos   int
}

func newLexer(input string) *lexer {
	return &lexer{input: strings.TrimSpace(input)}
}

func (l *lexer) next() token {
	// 跳过空白
	for l.pos < len(l.input) && unicode.IsSpace(rune(l.input[l.pos])) {
		l.pos++
	}

	if l.pos >= len(l.input) {
		return token{typ: tokEOF}
	}

	ch := l.input[l.pos]

	// 数字
	if unicode.IsDigit(rune(ch)) || ch == '.' {
		start := l.pos
		for l.pos < len(l.input) && (unicode.IsDigit(rune(l.input[l.pos])) || l.input[l.pos] == '.') {
			l.pos++
		}
		// 科学计数法
		if l.pos < len(l.input) && (l.input[l.pos] == 'e' || l.input[l.pos] == 'E') {
			l.pos++
			if l.pos < len(l.input) && (l.input[l.pos] == '+' || l.input[l.pos] == '-') {
				l.pos++
			}
			for l.pos < len(l.input) && unicode.IsDigit(rune(l.input[l.pos])) {
				l.pos++
			}
		}
		val := l.input[start:l.pos]
		num, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return token{typ: tokNumber, value: val, num: 0}
		}
		return token{typ: tokNumber, value: val, num: num}
	}

	// 字母：函数名或常量
	if unicode.IsLetter(rune(ch)) {
		start := l.pos
		for l.pos < len(l.input) && (unicode.IsLetter(rune(l.input[l.pos])) || unicode.IsDigit(rune(l.input[l.pos])) || l.input[l.pos] == '_') {
			l.pos++
		}
		name := l.input[start:l.pos]
		// 检查是否为已知常量
		switch strings.ToLower(name) {
		case "pi":
			return token{typ: tokNumber, value: name, num: math.Pi}
		case "e":
			return token{typ: tokNumber, value: name, num: math.E}
		default:
			// 如果是函数名，后面必须跟 (
			if l.pos < len(l.input) && l.input[l.pos] == '(' {
				return token{typ: tokFunc, value: name}
			}
			return token{typ: tokNumber, value: name, num: 0}
		}
	}

	l.pos++
	switch ch {
	case '+':
		return token{typ: tokPlus, value: "+"}
	case '-':
		return token{typ: tokMinus, value: "-"}
	case '*':
		return token{typ: tokMul, value: "*"}
	case '/':
		return token{typ: tokDiv, value: "/"}
	case '(':
		return token{typ: tokLParen, value: "("}
	case ')':
		return token{typ: tokRParen, value: ")"}
	case ',':
		return token{typ: tokComma, value: ","}
	default:
		return token{typ: tokPlus, value: string(ch)}
	}
}

// parser 递归下降解析器
type parser struct {
	lex    *lexer
	cur    token
	peek   token
	ctx    context.Context
}

func newParser(input string, ctx context.Context) *parser {
	p := &parser{lex: newLexer(input), ctx: ctx}
	p.advance()
	p.advance()
	return p
}

func (p *parser) advance() {
	p.cur = p.peek
	p.peek = p.lex.next()
}

// expr = term (('+' | '-') term)*
func (p *parser) parseExpr() (float64, error) {
	result, err := p.parseTerm()
	if err != nil {
		return 0, err
	}

	for p.cur.typ == tokPlus || p.cur.typ == tokMinus {
		op := p.cur
		p.advance()
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op.typ == tokPlus {
			result += right
		} else {
			result -= right
		}
	}
	return result, nil
}

// term = factor (('*' | '/') factor)*
func (p *parser) parseTerm() (float64, error) {
	result, err := p.parseFactor()
	if err != nil {
		return 0, err
	}

	for p.cur.typ == tokMul || p.cur.typ == tokDiv {
		op := p.cur
		p.advance()
		right, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op.typ == tokMul {
			result *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("除数不能为零")
			}
			result /= right
		}
	}
	return result, nil
}

// factor = ('+' | '-')? ( number | '(' expr ')' | func '(' expr ')' )
func (p *parser) parseFactor() (float64, error) {
	// 一元负号
	if p.cur.typ == tokMinus {
		p.advance()
		val, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}
	// 一元正号
	if p.cur.typ == tokPlus {
		p.advance()
		return p.parseFactor()
	}

	// 数字
	if p.cur.typ == tokNumber {
		val := p.cur.num
		p.advance()
		return val, nil
	}

	// 函数调用: func(expr)
	if p.cur.typ == tokFunc {
		funcName := strings.ToLower(p.cur.value)
		fn, ok := mathFuncs[funcName]
		if !ok {
			return 0, fmt.Errorf("不支持的函数: %s", funcName)
		}
		p.advance() // 跳过函数名
		if p.cur.typ != tokLParen {
			return 0, fmt.Errorf("函数 %s 后需要跟 (", funcName)
		}
		p.advance() // 跳过 (
		arg, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.cur.typ != tokRParen {
			return 0, fmt.Errorf("函数 %s 缺少闭合 )", funcName)
		}
		p.advance() // 跳过 )
		return fn(arg), nil
	}

	// 括号表达式: ( expr )
	if p.cur.typ == tokLParen {
		p.advance()
		val, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.cur.typ != tokRParen {
			return 0, fmt.Errorf("缺少闭合括号 )")
		}
		p.advance()
		return val, nil
	}

	return 0, fmt.Errorf("无法解析: %s", p.cur.value)
}

// Calculate 计算数学表达式，返回计算结果
func Calculate(expression string) (float64, error) {
	p := newParser(expression, nil)
	result, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.cur.typ != tokEOF {
		return 0, fmt.Errorf("表达式包含无法解析的内容: %s", p.cur.value)
	}
	return result, nil
}

// SystemInfoTool 系统信息工具
type SystemInfoTool struct{}

func NewSystemInfoTool() *SystemInfoTool {
	return &SystemInfoTool{}
}

func (t *SystemInfoTool) Name() string {
	return "system_info"
}

func (t *SystemInfoTool) Description() string {
	return "获取当前系统信息，包括操作系统、架构等"
}

func (t *SystemInfoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *SystemInfoTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	info := map[string]interface{}{
		"os":      runtime.GOOS,
		"arch":    runtime.GOARCH,
		"cpus":    runtime.NumCPU(),
		"version": runtime.Version(),
	}

	result, _ := json.MarshalIndent(info, "", "  ")
	return string(result), nil
}

// TimeTool 时间查询工具
type TimeTool struct{}

func NewTimeTool() *TimeTool {
	return &TimeTool{}
}

func (t *TimeTool) Name() string {
	return "current_time"
}

func (t *TimeTool) Description() string {
	return "获取当前日期和时间"
}

func (t *TimeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *TimeTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return fmt.Sprintf("当前时间: %s", runtime.GOOS), nil
}
