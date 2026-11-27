//go:build !darwin && !windows

package tray

// Options 与托盘实现保持一致，避免调用方出现平台分支。
type Options struct {
	// URL 展示在状态菜单项中，让用户确认服务地址。
	URL string
	// OnOpen 在用户点击「打开 Web 地址」时调用。
	OnOpen func()
	// OnQuit 在用户点击「退出」时调用，负责触发服务关闭。
	OnQuit func()
}

// Run 在没有托盘实现的平台上直接返回，由调用方回退到前台运行。
func Run(_ Options, _ <-chan struct{}) {}

// Supported 在无托盘实现的平台上返回 false。
func Supported() bool {
	return false
}
