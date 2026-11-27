//go:build windows

package tools

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// windowsGOOS 是 Windows 的 GOOS 取值。
	windowsGOOS = "windows"
	// fallbackShell 是候选解释器都不存在时的兜底选择。
	fallbackShell = "powershell.exe"
	// powershellProfile 是系统自带 Windows PowerShell 的相对路径。
	powershellProfile = `System32\WindowsPowerShell\v1.0\powershell.exe`
)

// powershellArgs 让 PowerShell 跳过用户配置、非交互地执行标准输入。
var powershellArgs = []string{
	"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-",
}

// shellCandidates 返回 Windows 上的解释器顺序：优先 PATH 中的 PowerShell 7
// （pwsh.exe），其次系统自带的 Windows PowerShell。两者都从标准输入读取脚本，
// 因此多行命令无需落盘临时文件。
func shellCandidates() []shellSpec {
	return []shellSpec{
		{path: "pwsh.exe", args: powershellArgs},
		{path: programFilesPowerShell(), args: powershellArgs},
		{path: systemPowerShell(), args: powershellArgs},
		{path: fallbackShell, args: powershellArgs},
	}
}

// programFilesPowerShell 返回未加入 PATH 的 PowerShell 7 默认安装路径。
func programFilesPowerShell() string {
	root := os.Getenv("ProgramFiles")
	if root == "" {
		root = `C:\Program Files`
	}
	return filepath.Join(root, "PowerShell", "7", "pwsh.exe")
}

// systemPowerShell 返回 SystemRoot 下的 Windows PowerShell 绝对路径。
func systemPowerShell() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, powershellProfile)
}

// fallbackShellSpec 在候选解释器全部缺失时仍使用 PowerShell 名称，
// 让启动失败如实暴露为可执行文件不存在。
func fallbackShellSpec() shellSpec {
	return shellSpec{path: fallbackShell, args: powershellArgs}
}

// pathPrefixLine 用 PowerShell 语法把目录放到 PATH 最前。
func pathPrefixLine(prefix string) string {
	return "$env:PATH = " + quoteForPowerShell(prefix) + " + ';' + $env:PATH"
}

// quoteForPowerShell 用单引号包裹路径，单引号按 PowerShell 规则双写转义。
func quoteForPowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
