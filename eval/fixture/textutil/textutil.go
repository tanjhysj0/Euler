// Package textutil 提供字符串切分与拼接工具。
package textutil

import (
	"strings"
	"unicode/utf8"
)

// Split 按 sep 切分 s。sep 为空时按 UTF-8 字符逐个切分，s 为空时返回 nil。
func Split(s, sep string) []string {
	if s == "" {
		return nil
	}
	if sep == "" {
		return splitRunes(s)
	}
	return strings.Split(s, sep)
}

// Join 用 sep 拼接 parts。
func Join(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

func splitRunes(s string) []string {
	out := make([]string, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}
