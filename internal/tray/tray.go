//go:build darwin || windows

// Package tray 在系统托盘（macOS 菜单栏 / Windows 通知区域）常驻一个状态图标，
// 让服务在没有终端窗口的情况下也能被用户看到并主动退出。
package tray

import (
	_ "embed"
	"runtime"

	"fyne.io/systray"
)

//go:embed assets/tray.png
var iconPNG []byte

//go:embed assets/tray.ico
var iconICO []byte

// 菜单文案集中定义，便于测试与后续调整。
const (
	itemStatus = "Euler 正在运行"
	itemOpen   = "打开 Web 地址"
	itemQuit   = "退出"
	tooltip    = "Euler 本地服务"
)

// Options 描述托盘菜单需要展示的地址与回调。
type Options struct {
	// URL 展示在状态菜单项中，让用户确认服务地址。
	URL string
	// OnOpen 在用户点击「打开 Web 地址」时调用。
	OnOpen func()
	// OnQuit 在用户点击「退出」时调用，负责触发服务关闭。
	OnQuit func()
}

// Run 在当前 goroutine 运行托盘事件循环，直到用户点击「退出」或 stop 被关闭。
// macOS 的 Cocoa 事件循环要求它在主 goroutine 中执行，因此调用方需把
// 常驻服务放到其他 goroutine。
func Run(opts Options, stop <-chan struct{}) {
	systray.Run(func() { onReady(opts, stop) }, func() {})
}

// Supported 表示当前平台是否提供托盘图标实现。
func Supported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

// onReady 在托盘就绪后构建菜单：首项展示运行状态，随后是打开与退出。
func onReady(opts Options, stop <-chan struct{}) {
	// Windows 的通知区域只接受 .ico，macOS 用 PNG 才能保留透明背景。
	if runtime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTooltip(tooltip)
	status := systray.AddMenuItem(itemStatus+" "+opts.URL, tooltip)
	// 状态项仅用于展示，不响应点击。
	status.Disable()
	systray.AddSeparator()
	openItem := systray.AddMenuItem(itemOpen, "在浏览器中打开 "+opts.URL)
	quitItem := systray.AddMenuItem(itemQuit, "停止服务并退出进程")
	go handleMenu(opts, stop, menuItems{open: openItem, quit: quitItem})
}

// menuItems 汇总需要在事件循环中监听的菜单项。
type menuItems struct {
	open *systray.MenuItem
	quit *systray.MenuItem
}

// handleMenu 持续消费菜单点击，直到托盘被外部关闭。
func handleMenu(opts Options, stop <-chan struct{}, items menuItems) {
	for {
		select {
		case <-items.open.ClickedCh:
			opts.OnOpen()
		case <-items.quit.ClickedCh:
			// 只取消服务上下文，等收尾完成后再由 stop 通道结束托盘。
			opts.OnQuit()
		case <-stop:
			systray.Quit()
			return
		}
	}
}
