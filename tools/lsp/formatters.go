package lsp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// FormatLocationStr 格式化位置信息为 file.go:42:10
func FormatLocationStr(uri string, line, character int) string {
	p := uriToFilePath(uri)
	if rel, err := filepath.Rel(".", p); err == nil && !strings.HasPrefix(rel, "..") {
		p = rel
	}
	return fmt.Sprintf("%s:%d:%d", p, line+1, character+1) // 0-based → 1-based
}

// FormatGoToDefinition 格式化跳转定义结果
func FormatGoToDefinition(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No definition found.", 0, 0
	}

	// 尝试解析为 Location 数组
	var locations []Location
	if err := json.Unmarshal(raw, &locations); err == nil {
		valid := filterValidLocations(locations)
		if len(valid) == 0 {
			return "No definition found.", 0, 0
		}
		if len(valid) == 1 {
			return fmt.Sprintf("Defined in %s",
				FormatLocationStr(valid[0].URI, valid[0].Range.Start.Line, valid[0].Range.Start.Character)), 1, 1
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d definitions:\n", len(valid)))
		for _, loc := range valid {
			sb.WriteString(fmt.Sprintf("  %s\n",
				FormatLocationStr(loc.URI, loc.Range.Start.Line, loc.Range.Start.Character)))
		}
		return sb.String(), len(valid), countUniqueURIs(valid)
	}

	// 单条 Location
	var loc Location
	if err := json.Unmarshal(raw, &loc); err == nil && loc.URI != "" {
		return fmt.Sprintf("Defined in %s",
			FormatLocationStr(loc.URI, loc.Range.Start.Line, loc.Range.Start.Character)), 1, 1
	}

	return "No definition found.", 0, 0
}

// FormatFindReferences 格式化引用查找结果
func FormatFindReferences(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No references found.", 0, 0
	}

	var locations []Location
	if err := json.Unmarshal(raw, &locations); err != nil || len(locations) == 0 {
		return "No references found.", 0, 0
	}

	valid := filterValidLocations(locations)
	if len(valid) == 0 {
		return "No references found.", 0, 0
	}
	if len(valid) == 1 {
		return fmt.Sprintf("Found 1 reference:\n  %s",
			FormatLocationStr(valid[0].URI, valid[0].Range.Start.Line, valid[0].Range.Start.Character)), 1, 1
	}

	// 按文件分组
	grouped := groupByURI(valid)
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d references across %d files:", len(valid), len(grouped)))

	// 排序以保持稳定输出
	var uris []string
	for uri := range grouped {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	for _, uri := range uris {
		p := FormatLocationStr(uri, 0, 0)
		// 只用路径，去掉行号
		if idx := strings.LastIndex(p, ":"); idx > 0 {
			p = p[:idx]
			if idx2 := strings.LastIndex(p, ":"); idx2 > 0 {
				p = p[:idx2]
			}
		}
		lines = append(lines, fmt.Sprintf("\n%s:", p))
		for _, loc := range grouped[uri] {
			lines = append(lines, fmt.Sprintf("  Line %d:%d",
				loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
	}

	return strings.Join(lines, "\n"), len(valid), len(grouped)
}

// FormatHover 格式化悬停信息
func FormatHover(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No hover information available.", 0, 0
	}

	var hover Hover
	if err := json.Unmarshal(raw, &hover); err != nil {
		return "No hover information available.", 0, 0
	}

	content := extractHoverContent(hover.Contents)
	if content == "" {
		return "No hover information available.", 0, 0
	}

	if hover.Range != nil {
		return fmt.Sprintf("Hover info at %d:%d:\n\n%s",
			hover.Range.Start.Line+1, hover.Range.Start.Character+1, content), 1, 1
	}
	return content, 1, 1
}

// FormatDocumentSymbol 格式化文档符号（支持层级 DocumentSymbol 和扁平 SymbolInformation）
func FormatDocumentSymbol(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No symbols found in document.", 0, 0
	}

	// 先尝试层级 DocumentSymbol[]
	var docSyms []DocumentSymbol
	if err := json.Unmarshal(raw, &docSyms); err == nil && len(docSyms) > 0 {
		var lines []string
		lines = append(lines, "Document symbols:")
		for _, sym := range docSyms {
			lines = append(lines, formatDocSymbolNode(sym, 0)...)
		}
		count := countDocSymbols(docSyms)
		return strings.Join(lines, "\n"), count, 1
	}

	// 回退到扁平 SymbolInformation[]
	var symInfos []SymbolInformation
	if err := json.Unmarshal(raw, &symInfos); err == nil && len(symInfos) > 0 {
		return formatSymbolInformations(symInfos, "workspace")
	}

	return "No symbols found in document.", 0, 0
}

// FormatWorkspaceSymbol 格式化工作区符号搜索结果
func FormatWorkspaceSymbol(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No symbols found in workspace.", 0, 0
	}

	var symbols []SymbolInformation
	if err := json.Unmarshal(raw, &symbols); err != nil || len(symbols) == 0 {
		return "No symbols found in workspace.", 0, 0
	}

	return formatSymbolInformations(symbols, "workspace")
}

// FormatPrepareCallHierarchy 格式化调用层次准备结果
func FormatPrepareCallHierarchy(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No call hierarchy item found at this position.", 0, 0
	}

	var items []CallHierarchyItem
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return "No call hierarchy item found at this position.", 0, 0
	}

	if len(items) == 1 {
		return fmt.Sprintf("Call hierarchy item: %s", formatCallItem(items[0])), 1, 1
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d call hierarchy items:", len(items)))
	for _, item := range items {
		lines = append(lines, "  "+formatCallItem(item))
	}
	return strings.Join(lines, "\n"), len(items), countUniqueURIsFromCallItems(items)
}

// FormatIncomingCalls 格式化传入调用结果
func FormatIncomingCalls(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No incoming calls found (nothing calls this function).", 0, 0
	}

	var calls []CallHierarchyIncomingCall
	if err := json.Unmarshal(raw, &calls); err != nil || len(calls) == 0 {
		return "No incoming calls found.", 0, 0
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d incoming %s:", len(calls), pluralize(len(calls), "call")))

	grouped := groupCallsByURI(calls)
	var uris []string
	for uri := range grouped {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	for _, uri := range uris {
		p := shortenPath(uriToFilePath(uri))
		lines = append(lines, fmt.Sprintf("\n%s:", p))
		for _, call := range grouped[uri] {
			kind := SymbolKindNames[call.From.Kind]
			line := call.From.Range.Start.Line + 1
			callLine := fmt.Sprintf("  %s (%s) - Line %d", call.From.Name, kind, line)
			if len(call.FromRanges) > 0 {
				var sites []string
				for _, r := range call.FromRanges {
					sites = append(sites, fmt.Sprintf("%d:%d", r.Start.Line+1, r.Start.Character+1))
				}
				callLine += fmt.Sprintf(" [calls at: %s]", strings.Join(sites, ", "))
			}
			lines = append(lines, callLine)
		}
	}

	fileCount := countUniqueURIsFromCallGroup(grouped)
	return strings.Join(lines, "\n"), len(calls), fileCount
}

// FormatOutgoingCalls 格式化传出调用结果
func FormatOutgoingCalls(raw json.RawMessage, workDir string) (string, int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "No outgoing calls found (this function calls nothing).", 0, 0
	}

	var calls []CallHierarchyOutgoingCall
	if err := json.Unmarshal(raw, &calls); err != nil || len(calls) == 0 {
		return "No outgoing calls found.", 0, 0
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d outgoing %s:", len(calls), pluralize(len(calls), "call")))

	grouped := groupOutgoingCallsByURI(calls)
	var uris []string
	for uri := range grouped {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	for _, uri := range uris {
		p := shortenPath(uriToFilePath(uri))
		lines = append(lines, fmt.Sprintf("\n%s:", p))
		for _, call := range grouped[uri] {
			kind := SymbolKindNames[call.To.Kind]
			line := call.To.Range.Start.Line + 1
			callLine := fmt.Sprintf("  %s (%s) - Line %d", call.To.Name, kind, line)
			if len(call.FromRanges) > 0 {
				var sites []string
				for _, r := range call.FromRanges {
					sites = append(sites, fmt.Sprintf("%d:%d", r.Start.Line+1, r.Start.Character+1))
				}
				callLine += fmt.Sprintf(" [called from: %s]", strings.Join(sites, ", "))
			}
			lines = append(lines, callLine)
		}
	}

	fileCount := countUniqueURIsFromOutgoingCallGroup(grouped)
	return strings.Join(lines, "\n"), len(calls), fileCount
}

// ---------------------------------------------------------------------------
// 内部辅助函数
// ---------------------------------------------------------------------------

func filterValidLocations(locations []Location) []Location {
	var valid []Location
	for _, loc := range locations {
		if loc.URI != "" {
			valid = append(valid, loc)
		}
	}
	return valid
}

func countUniqueURIs(locations []Location) int {
	seen := make(map[string]bool)
	for _, loc := range locations {
		if loc.URI != "" {
			seen[loc.URI] = true
		}
	}
	return len(seen)
}

func groupByURI(locations []Location) map[string][]Location {
	grouped := make(map[string][]Location)
	for _, loc := range locations {
		grouped[loc.URI] = append(grouped[loc.URI], loc)
	}
	return grouped
}

func extractHoverContent(contents interface{}) string {
	switch c := contents.(type) {
	case string:
		return c
	case map[string]interface{}:
		if val, ok := c["value"].(string); ok {
			return val
		}
	case []interface{}:
		var parts []string
		for _, item := range c {
			switch m := item.(type) {
			case string:
				parts = append(parts, m)
			case map[string]interface{}:
				if val, ok := m["value"].(string); ok {
					parts = append(parts, val)
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func formatDocSymbolNode(sym DocumentSymbol, indent int) []string {
	kind := SymbolKindNames[sym.Kind]
	if kind == "" {
		kind = "Unknown"
	}
	prefix := strings.Repeat("  ", indent)
	line := fmt.Sprintf("%s%s (%s) - Line %d", prefix, sym.Name, kind, sym.Range.Start.Line+1)
	if sym.Detail != "" {
		line += fmt.Sprintf(" %s", sym.Detail)
	}
	var lines []string
	lines = append(lines, line)
	for _, child := range sym.Children {
		lines = append(lines, formatDocSymbolNode(child, indent+1)...)
	}
	return lines
}

func countDocSymbols(syms []DocumentSymbol) int {
	count := len(syms)
	for _, sym := range syms {
		count += countDocSymbols(sym.Children)
	}
	return count
}

func formatSymbolInformations(syms []SymbolInformation, scope string) (string, int, int) {
	// 按文件分组
	grouped := make(map[string][]SymbolInformation)
	for _, sym := range syms {
		if sym.Location.URI != "" {
			grouped[sym.Location.URI] = append(grouped[sym.Location.URI], sym)
		}
	}

	var lines []string
	if scope == "workspace" {
		lines = append(lines, fmt.Sprintf("Found %d %s in workspace:", len(syms), pluralize(len(syms), "symbol")))
	} else {
		lines = append(lines, fmt.Sprintf("Found %d %s:", len(syms), pluralize(len(syms), "symbol")))
	}

	var uris []string
	for uri := range grouped {
		uris = append(uris, uri)
	}
	sort.Strings(uris)

	for _, uri := range uris {
		p := shortenPath(uriToFilePath(uri))
		lines = append(lines, fmt.Sprintf("\n%s:", p))
		for _, sym := range grouped[uri] {
			kind := SymbolKindNames[sym.Kind]
			symLine := fmt.Sprintf("  %s (%s) - Line %d", sym.Name, kind, sym.Location.Range.Start.Line+1)
			if sym.ContainerName != "" {
				symLine += fmt.Sprintf(" in %s", sym.ContainerName)
			}
			lines = append(lines, symLine)
		}
	}

	return strings.Join(lines, "\n"), len(syms), len(grouped)
}

func formatCallItem(item CallHierarchyItem) string {
	kind := SymbolKindNames[item.Kind]
	return fmt.Sprintf("%s (%s) - %s:%d", item.Name, kind,
		shortenPath(uriToFilePath(item.URI)), item.Range.Start.Line+1)
}

func shortenPath(p string) string {
	if rel, err := filepath.Rel(".", p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

func pluralize(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func groupCallsByURI(calls []CallHierarchyIncomingCall) map[string][]CallHierarchyIncomingCall {
	grouped := make(map[string][]CallHierarchyIncomingCall)
	for _, call := range calls {
		uri := call.From.URI
		if uri != "" {
			grouped[uri] = append(grouped[uri], call)
		}
	}
	return grouped
}

func groupOutgoingCallsByURI(calls []CallHierarchyOutgoingCall) map[string][]CallHierarchyOutgoingCall {
	grouped := make(map[string][]CallHierarchyOutgoingCall)
	for _, call := range calls {
		uri := call.To.URI
		if uri != "" {
			grouped[uri] = append(grouped[uri], call)
		}
	}
	return grouped
}

func countUniqueURIsFromCallItems(items []CallHierarchyItem) int {
	seen := make(map[string]bool)
	for _, item := range items {
		if item.URI != "" {
			seen[item.URI] = true
		}
	}
	return len(seen)
}

func countUniqueURIsFromCallGroup(grouped map[string][]CallHierarchyIncomingCall) int {
	return len(grouped)
}

func countUniqueURIsFromOutgoingCallGroup(grouped map[string][]CallHierarchyOutgoingCall) int {
	return len(grouped)
}
