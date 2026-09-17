// Package strutil 提供 UTF-8 安全的字符串截断工具。
//
// 背景：Go 中 len(s) 与 s[i:j] 都是按「字节」计算的。对包含多字节字符
// （如中文）的字符串直接切片，可能切在字符中间，产生「非法 UTF-8」字节序列，
// 进而导致：
//   - 终端 / 日志显示乱码（出现 ）
//   - 下游文本工具（sed / grep 等）在 UTF-8 locale 下报 "illegal byte sequence"
//   - JSON 序列化时非法字节被替换为 U+FFFD
//
// 本包提供两种截断方式，均保证结果始终是合法 UTF-8：
//   - TruncateRunes：按字符数截断（适用于展示类的长度限制）
//   - TruncateBytes：按字节数截断（保留按大小限制的语义，但回退到合法字符边界）
package strutil

import "unicode/utf8"

// TruncateRunes 按「字符数」截断 s。
//
// 若字符数超过 maxRunes，返回前 maxRunes 个字符并追加 suffix；否则原样返回。
// maxRunes <= 0 表示不截断。结果始终是合法 UTF-8。
func TruncateRunes(s string, maxRunes int, suffix string) string {
	if maxRunes <= 0 || len(s) <= maxRunes {
		// 字节数已 <= maxRunes 时，字符数必然也 <= maxRunes，无需转换
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + suffix
}

// TruncateBytes 按「字节数」截断 s（保留按大小的限制语义），
// 但会回退到合法的字符起始边界，避免切出非法 UTF-8。
//
// 若字节数超过 maxBytes，返回截断后的前缀并追加 suffix；否则原样返回。
// maxBytes <= 0 表示不截断。
func TruncateBytes(s string, maxBytes int, suffix string) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	// 回退到字符起始字节（跳过 UTF-8 续字节 0b10xxxxxx）
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}
