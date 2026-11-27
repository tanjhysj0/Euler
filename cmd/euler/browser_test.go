package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrowserCommand(t *testing.T) {
	target := "http://127.0.0.1:8080"
	cases := []struct {
		goos string
		name string
		args []string
	}{
		{goos: "darwin", name: "open", args: []string{target}},
		{goos: "windows", name: "rundll32", args: []string{"url.dll,FileProtocolHandler", target}},
		{goos: "linux", name: "xdg-open", args: []string{target}},
	}
	for _, c := range cases {
		name, args := browserCommand(c.goos, target)
		require.Equal(t, c.name, name, "goos=%s", c.goos)
		require.Equal(t, c.args, args, "goos=%s", c.goos)
	}
}

func TestUIURL(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{listen: "127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{listen: "0.0.0.0:9000", want: "http://127.0.0.1:9000"},
		{listen: "[::]:8080", want: "http://127.0.0.1:8080"},
		{listen: "[::1]:8080", want: "http://[::1]:8080"},
	}
	for _, c := range cases {
		got, err := uiURL(c.listen)
		require.NoError(t, err, "listen=%s", c.listen)
		require.Equal(t, c.want, got)
	}
}

func TestUIURLRejectsInvalidListen(t *testing.T) {
	_, err := uiURL("8080")
	require.Error(t, err)
}

func TestEnvBool(t *testing.T) {
	cases := map[string]bool{
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true,
		"":      false,
		"0":     false,
		"false": false,
	}
	for value, want := range cases {
		t.Setenv(envOpenBrowser, value)
		require.Equal(t, want, envBool(envOpenBrowser), "value=%q", value)
	}
}
