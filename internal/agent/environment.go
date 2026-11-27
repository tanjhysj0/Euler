package agent

import (
	"runtime"

	"euler/internal/tools"
)

const (
	// windowsGOOS 是 Windows 的 GOOS 取值，用于选择解释器语法描述。
	windowsGOOS = "windows"
	// loginFlag 是登录 shell 的启动参数，用于说明 PATH 来源。
	loginFlag = "-l"
)

// platformLabel 返回当前运行平台，如 darwin/arm64。二进制在目标机器或
// 容器内直接运行，因此该值就是 Agent 实际所在的平台。
func platformLabel() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// shellLabel 描述命令工具实际使用的解释器与语法，避免模型猜测平台。
func shellLabel() string {
	shell, args := tools.CommandInterpreter()
	if runtime.GOOS == windowsGOOS {
		return "PowerShell（" + shell + "），命令使用 PowerShell 语法"
	}
	if hasLoginFlag(args) {
		return shell + "（登录模式），命令使用 POSIX sh 兼容语法"
	}
	return shell + "，命令使用 POSIX sh 兼容语法"
}

// hasLoginFlag 判断解释器是否以登录模式启动。
func hasLoginFlag(args []string) bool {
	for _, arg := range args {
		if arg == loginFlag {
			return true
		}
	}
	return false
}
