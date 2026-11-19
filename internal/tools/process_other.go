//go:build !darwin

package tools

import "os/exec"

// configureProcessGroup has no adapter outside macOS: a command whose children
// cannot be killed together with it is refused instead of started, which keeps
// the platform boundary explicit.
func configureProcessGroup(*exec.Cmd) error { return ErrUnsupportedPlatform }
