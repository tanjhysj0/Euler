package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// envOpenBrowser 是安装包桌面快捷方式使用的变量名：设置后等同于 -open。
const envOpenBrowser = "EULER_OPEN"

// browserCommand 返回指定操作系统下打开 URL 的命令与参数。
// 抽成纯函数便于测试覆盖三个平台分支。
func browserCommand(goos, target string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return "xdg-open", []string{target}
	}
}

// uiURL 把监听地址转换为浏览器可访问的本机 URL；监听全部网卡时回退到回环地址。
func uiURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("解析监听地址 %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	default:
		// 其他主机保持原值，便于用户指定回环地址的别名。
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}

// openUI 用系统默认浏览器打开本地界面；失败只记录日志，不影响服务。
func openUI(listen string) {
	target, err := uiURL(listen)
	if err != nil {
		slog.Warn("skip opening browser", logKeyError, err)
		return
	}
	name, args := browserCommand(runtime.GOOS, target)
	// 命令与参数全部由本函数生成，不包含外部输入。
	cmd := exec.Command(name, args...) //nolint:gosec // 固定候选命令
	if err = cmd.Start(); err != nil {
		slog.Warn("open browser failed", logKeyError, err, "url", target)
		return
	}
	slog.Info("opened browser", "url", target)
}

// envBool 读取布尔环境变量，仅 1/true/yes 视为开启。
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
