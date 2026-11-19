package logging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactReplacesKnownSecrets(t *testing.T) {
	got := Redact("model call failed with sk-live-123", "sk-live-123")
	require.Equal(t, "model call failed with [REDACTED]", got)
}

func TestRedactIgnoresEmptySecrets(t *testing.T) {
	got := Redact("plain text without secrets", "")
	require.Equal(t, "plain text without secrets", got)
}

func TestRedactMasksKeyValueSecrets(t *testing.T) {
	input := `Authorization: Bearer sk-abc api_key=sk-def Cookie: session=xyz`
	got := Redact(input)
	require.NotContains(t, got, "sk-abc")
	require.NotContains(t, got, "sk-def")
	require.NotContains(t, got, "xyz")
	require.Contains(t, got, "[REDACTED]")
}

func TestRedactKeepsNonSecretText(t *testing.T) {
	got := Redact(`{"message":"bad request"}`)
	require.Equal(t, `{"message":"bad request"}`, got)
}
