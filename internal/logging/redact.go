// Package logging 提供辅助函数，防止机密出现在诊断输出中。
package logging

import (
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var keyValueSecret = regexp.MustCompile(
	`(?i)(authorization|api_key|cookie)(["\s:=]+(?:bearer\s+)?)[^\s,"}]+`,
)

// Redact 将每个非空机密替换为占位符，并遮蔽常见的
// Authorization、api_key 和 cookie 键值形式。
func Redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redactedValue)
		}
	}
	return keyValueSecret.ReplaceAllString(text, `${1}${2}`+redactedValue)
}
