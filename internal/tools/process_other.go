//go:build !darwin && !linux && !windows

package tools

import "os/exec"

// configureProcessGroup 在 darwin/linux 之外没有适配器：若命令的子进程
// 无法与其一并杀死，则拒绝执行而不是启动，从而明确平台边界。
func configureProcessGroup(*exec.Cmd) error { return ErrUnsupportedPlatform }
