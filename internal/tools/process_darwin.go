//go:build darwin

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

// processWaitDelay is how long the parent waits for stragglers after the
// process group has been sent SIGKILL.
const processWaitDelay = 2 * time.Second

// configureProcessGroup runs the shell in its own process group and answers an
// interrupted command by killing that whole group, so background children
// cannot outlive a cancelled or timed-out command.
func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = processWaitDelay
	return nil
}
