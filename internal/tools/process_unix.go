//go:build darwin || linux

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

// processWaitDelay 是向进程组发送 SIGKILL 后，
// 父进程等待残余进程退出的时长。
const processWaitDelay = 2 * time.Second

// configureProcessGroup 让 shell 在独立的进程组中运行，命令被中断时
// 杀死整个进程组，使后台子进程无法在被取消或超时的命令之后存活。
func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = processWaitDelay
	return nil
}
