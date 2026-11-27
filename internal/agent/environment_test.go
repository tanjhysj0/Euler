package agent

import (
	"runtime"
	"strings"
	"testing"
)

func TestPlatformLabelMatchesRuntime(t *testing.T) {
	want := runtime.GOOS + "/" + runtime.GOARCH
	if got := platformLabel(); got != want {
		t.Fatalf("平台描述应为 %q，实际 %q", want, got)
	}
}

func TestShellLabelDescribesCommandSyntax(t *testing.T) {
	label := shellLabel()
	if !strings.Contains(label, "语法") {
		t.Fatalf("解释器描述应说明命令语法: %q", label)
	}
	if runtime.GOOS == windowsGOOS && !strings.Contains(label, "PowerShell") {
		t.Fatalf("Windows 应说明 PowerShell 语法: %q", label)
	}
	if runtime.GOOS != windowsGOOS && !strings.Contains(label, "POSIX") {
		t.Fatalf("类 Unix 平台应说明 POSIX 语法: %q", label)
	}
}

func TestHasLoginFlag(t *testing.T) {
	if !hasLoginFlag([]string{loginFlag, "-s"}) {
		t.Fatal("应识别登录模式参数")
	}
	if hasLoginFlag([]string{"-s"}) {
		t.Fatal("无登录参数时不应判定为登录模式")
	}
}
