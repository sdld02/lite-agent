// Package lsp 实现 LSP (Language Server Protocol) 工具，
// 将 LSP 代码智能能力集成到 AI Agent 中。
//
// 设计理念参考 Claude Code LSPTool：
//   - 单一工具 + 多操作模式（Operation Discriminated Union）
//   - 扩展名路由（extension → LSP Server）
//   - 防御性编程（层层过滤无效数据）
//   - 坐标系转换（1-based ↔ 0-based 透明）
//   - 惰性启动（按需启动 LSP 子进程）
//   - gitignore 感知的结果过滤
package lsp

// Operation 定义 LSP 工具支持的操作类型
type Operation string

const (
	OpGoToDefinition       Operation = "goToDefinition"
	OpFindReferences       Operation = "findReferences"
	OpHover                Operation = "hover"
	OpDocumentSymbol       Operation = "documentSymbol"
	OpWorkspaceSymbol      Operation = "workspaceSymbol"
	OpGoToImplementation   Operation = "goToImplementation"
	OpPrepareCallHierarchy Operation = "prepareCallHierarchy"
	OpIncomingCalls        Operation = "incomingCalls"
	OpOutgoingCalls        Operation = "outgoingCalls"
)

// AllOperations 所有支持的操作列表
var AllOperations = []Operation{
	OpGoToDefinition,
	OpFindReferences,
	OpHover,
	OpDocumentSymbol,
	OpWorkspaceSymbol,
	OpGoToImplementation,
	OpPrepareCallHierarchy,
	OpIncomingCalls,
	OpOutgoingCalls,
}

// IsValidOperation 检查 operation 是否有效
func IsValidOperation(op string) bool {
	for _, o := range AllOperations {
		if string(o) == op {
			return true
		}
	}
	return false
}

// IsPositionBased 检查 operation 是否需要位置参数
func (op Operation) IsPositionBased() bool {
	switch op {
	case OpGoToDefinition, OpFindReferences, OpHover, OpGoToImplementation,
		OpPrepareCallHierarchy, OpIncomingCalls, OpOutgoingCalls:
		return true
	default:
		return false
	}
}

// LSPToolInput 工具输入参数
type LSPToolInput struct {
	Operation Operation `json:"operation"`
	FilePath  string    `json:"filePath"`
	Line      int       `json:"line"`
	Character int       `json:"character"`
}

// LSPToolOutput 工具输出结果
type LSPToolOutput struct {
	Operation   Operation `json:"operation"`
	Result      string    `json:"result"`
	FilePath    string    `json:"filePath"`
	ResultCount int       `json:"resultCount,omitempty"`
	FileCount   int       `json:"fileCount,omitempty"`
}

// ---------------------------------------------------------------------------
// 简化的 LSP 协议类型（仅包含本工具需要的字段）
// ---------------------------------------------------------------------------

// Position LSP 位置
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range LSP 范围
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location LSP 位置引用
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// LocationLink LSP 位置链接（部分服务器返回此格式）
type LocationLink struct {
	TargetURI            string `json:"targetUri"`
	TargetRange          Range  `json:"targetRange"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

// SymbolKind LSP 符号类型枚举
type SymbolKind int

// SymbolKind 常量
const (
	SymbolKindFile          SymbolKind = 1
	SymbolKindModule        SymbolKind = 2
	SymbolKindNamespace     SymbolKind = 3
	SymbolKindPackage       SymbolKind = 4
	SymbolKindClass         SymbolKind = 5
	SymbolKindMethod        SymbolKind = 6
	SymbolKindProperty      SymbolKind = 7
	SymbolKindField         SymbolKind = 8
	SymbolKindConstructor   SymbolKind = 9
	SymbolKindEnum          SymbolKind = 10
	SymbolKindInterface     SymbolKind = 11
	SymbolKindFunction      SymbolKind = 12
	SymbolKindVariable      SymbolKind = 13
	SymbolKindConstant      SymbolKind = 14
	SymbolKindString        SymbolKind = 15
	SymbolKindNumber        SymbolKind = 16
	SymbolKindBoolean       SymbolKind = 17
	SymbolKindArray         SymbolKind = 18
	SymbolKindObject        SymbolKind = 19
	SymbolKindKey           SymbolKind = 20
	SymbolKindNull          SymbolKind = 21
	SymbolKindEnumMember    SymbolKind = 22
	SymbolKindStruct        SymbolKind = 23
	SymbolKindEvent         SymbolKind = 24
	SymbolKindOperator      SymbolKind = 25
	SymbolKindTypeParameter SymbolKind = 26
)

// SymbolKindNames 符号类型名称映射
var SymbolKindNames = map[SymbolKind]string{
	SymbolKindFile:          "File",
	SymbolKindModule:        "Module",
	SymbolKindNamespace:     "Namespace",
	SymbolKindPackage:       "Package",
	SymbolKindClass:         "Class",
	SymbolKindMethod:        "Method",
	SymbolKindProperty:      "Property",
	SymbolKindField:         "Field",
	SymbolKindConstructor:   "Constructor",
	SymbolKindEnum:          "Enum",
	SymbolKindInterface:     "Interface",
	SymbolKindFunction:      "Function",
	SymbolKindVariable:      "Variable",
	SymbolKindConstant:      "Constant",
	SymbolKindString:        "String",
	SymbolKindNumber:        "Number",
	SymbolKindBoolean:       "Boolean",
	SymbolKindArray:         "Array",
	SymbolKindObject:        "Object",
	SymbolKindKey:           "Key",
	SymbolKindNull:          "Null",
	SymbolKindEnumMember:    "EnumMember",
	SymbolKindStruct:        "Struct",
	SymbolKindEvent:         "Event",
	SymbolKindOperator:      "Operator",
	SymbolKindTypeParameter: "TypeParameter",
}

// SymbolInformation LSP 符号信息（workspace/symbol 返回）
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	Location      Location   `json:"location"`
	ContainerName string     `json:"containerName,omitempty"`
}

// DocumentSymbol LSP 文档符号（textDocument/documentSymbol 返回的层级格式）
type DocumentSymbol struct {
	Name     string           `json:"name"`
	Detail   string           `json:"detail,omitempty"`
	Kind     SymbolKind       `json:"kind"`
	Range    Range            `json:"range"`
	Children []DocumentSymbol `json:"children,omitempty"`
}

// Hover LSP 悬停信息
type Hover struct {
	Contents interface{} `json:"contents"` // string | MarkupContent | []MarkedString
	Range    *Range      `json:"range,omitempty"`
}

// MarkupContent LSP 标记内容
type MarkupContent struct {
	Kind  string `json:"kind"` // "markdown" | "plaintext"
	Value string `json:"value"`
}

// MarkedString LSP 标记字符串
type MarkedString struct {
	Language string `json:"language"`
	Value    string `json:"value"`
}

// CallHierarchyItem LSP 调用层次项
type CallHierarchyItem struct {
	Name           string     `json:"name"`
	Kind           SymbolKind `json:"kind"`
	URI            string     `json:"uri"`
	Range          Range      `json:"range"`
	SelectionRange Range      `json:"selectionRange"`
	Detail         string     `json:"detail,omitempty"`
}

// CallHierarchyIncomingCall LSP 传入调用
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCall LSP 传出调用
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// TextDocumentIdentifier LSP 文本文档标识
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}
