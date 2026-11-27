//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const (
	// processWaitDelay 是结束进程树后，父进程等待残余进程退出的时长。
	processWaitDelay = 2 * time.Second
	// createNewProcessGroup 让 shell 拥有独立的控制台进程组，
	// 父进程收到控制台事件时不会直接波及 shell 及其子进程。
	createNewProcessGroup = 0x00000200
)

// configureProcessGroup 让 PowerShell 在独立进程组中运行；命令被取消或超时时，
// 用 taskkill 结束整棵进程树，避免后台子进程继续存活。
func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := strconv.Itoa(cmd.Process.Pid)
		//nolint:gosec // PID 来自本进程启动的子进程，taskkill 是固定程序。
		kill := exec.Command("taskkill", "/T", "/F", "/PID", pid)
		if err := kill.Run(); err != nil {
			// 进程可能已自行退出，回退到仅结束父进程。
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = processWaitDelay
	return nil
}
