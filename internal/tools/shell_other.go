//go:build !darwin && !linux && !windows

package tools

const (
	// fallbackShell 是候选解释器都不存在时的兜底选择。
	fallbackShell = "/bin/sh"
)

// shellCandidates 在其他平台返回空列表：configureProcessGroup 会拒绝执行。
func shellCandidates() []shellSpec { return nil }

// fallbackShellSpec 返回兜底解释器，实际执行仍会被平台适配器拒绝。
func fallbackShellSpec() shellSpec {
	return shellSpec{path: fallbackShell, args: []string{stdinFlag}}
}

// pathPrefixLine 在其他平台不注入 PATH 前缀。
func pathPrefixLine(_ string) string { return emptyText }
