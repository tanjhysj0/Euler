// Package logging provides helpers that keep secrets out of diagnostic output.
package logging

import (
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var keyValueSecret = regexp.MustCompile(
	`(?i)(authorization|api_key|cookie)(["\s:=]+(?:bearer\s+)?)[^\s,"}]+`,
)

// Redact replaces every non-empty secret with a placeholder and masks common
// Authorization, api_key and cookie key/value forms.
func Redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redactedValue)
		}
	}
	return keyValueSecret.ReplaceAllString(text, `${1}${2}`+redactedValue)
}
