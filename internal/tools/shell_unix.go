//go:build darwin || linux

package tools

import (
	"runtime"
	"strings"
)

const (
	// fallbackShell 是候选解释器都不存在时的兜底选择。
	fallbackShell = "/bin/sh"
)

// shellCandidates 返回当前平台优先使用的解释器顺序。评测任务跑在 Linux
// 容器里，因此以 bash/sh 为主；macOS 上保留 zsh 登录 shell 以读取用户 PATH。
func shellCandidates() []shellSpec {
	if runtime.GOOS == darwinGOOS {
		return []shellSpec{
			{path: "/bin/zsh", args: loginArgs},
			{path: "/bin/bash", args: loginArgs},
			{path: "/bin/sh", args: stdinArgs},
		}
	}
	return []shellSpec{
		{path: "/bin/bash", args: loginArgs},
		{path: "/bin/sh", args: stdinArgs},
	}
}

// fallbackShellSpec 在候选解释器全部缺失时使用 /bin/sh。
func fallbackShellSpec() shellSpec {
	return shellSpec{path: fallbackShell, args: stdinArgs}
}

// pathPrefixLine 用 POSIX 语法把目录放到 PATH 最前。
func pathPrefixLine(prefix string) string {
	return "export PATH=" + quoteForShell(prefix) + ":$PATH"
}

// quoteForShell 用单引号包裹路径，供 shell 安全展开。
func quoteForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
