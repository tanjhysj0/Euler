// Package tools implements the host-side tools the agent can invoke.
package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"euler/internal/approval"
)

const (
	// defaultShell is the macOS login shell that runs every command. The
	// interpreter path stays a compile-time constant so the launched process is
	// fixed, and the command text travels over standard input instead of as an
	// argument, which keeps a model-supplied command out of argv.
	defaultShell = "/bin/zsh"
	// loginFlag starts a login shell so the user's PATH setup applies.
	loginFlag = "-l"
	// stdinFlag reads the command from the shell's standard input.
	stdinFlag = "-s"
	// envAssignment separates an environment key from its value.
	envAssignment = "="
	// emptyCapacity builds slices with a preset length.
	emptyCapacity = 0
	// noTimeout means "inherit the configured deadline".
	noTimeout time.Duration = 0
)

var (
	// ErrUnsupportedPlatform marks host execution that has no adapter here.
	ErrUnsupportedPlatform = errors.New("shell execution unsupported on this platform")
)

// ShellConfig bounds every command of one executor.
type ShellConfig struct {
	Timeout        time.Duration
	MaxOutputBytes int
}

// ShellRequest is one host command. Dir is the workspace directory shown in the
// approval card; it is convenience, not a security boundary. Timeout overrides
// the configured deadline for this command only, and SecretNames lists the
// environment variables referencing model credentials to strip.
type ShellRequest struct {
	Dir         string        `json:"dir,omitempty"`
	Command     string        `json:"command"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	SecretNames []string      `json:"secret_names,omitempty"`
}

// ShellResult is the outcome of one command. Err is the Go error explaining a
// failed or interrupted run: context.DeadlineExceeded when the timeout fired,
// context.Canceled when the caller gave up, otherwise the start failure.
type ShellResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
	Err       error  `json:"-"`
}

// GuardedCall pairs one approval request with the command it authorises.
type GuardedCall struct {
	Approval approval.Request
	Shell    ShellRequest
}

// Shell executes host commands through the system login shell.
type Shell struct{ config ShellConfig }

// NewShellExecutor builds one Shell from configuration.
func NewShellExecutor(config ShellConfig) *Shell { return &Shell{config: config} }

// Execute runs the command as a login-shell script read from standard input,
// inside request.Dir, with every credential reference stripped from the child
// environment. Timeout or cancellation kills the whole process group, and the
// combined output is truncated at the configured maximum.
func (s *Shell) Execute(ctx context.Context, request ShellRequest) ShellResult {
	shellCtx, cancel := context.WithTimeout(ctx, s.timeout(request.Timeout))
	defer cancel()
	output := &limitedWriter{limit: s.config.MaxOutputBytes}
	cmd := exec.CommandContext(shellCtx, defaultShell, loginFlag, stdinFlag)
	cmd.Dir = request.Dir
	cmd.Env = SanitizeEnvironment(os.Environ(), request.SecretNames)
	cmd.Stdin = strings.NewReader(request.Command)
	cmd.Stdout, cmd.Stderr = output, output
	if err := configureProcessGroup(cmd); err != nil {
		return ShellResult{Err: err}
	}
	return collect(shellCtx, output, cmd.Run())
}

// RunGuarded asks gate for a decision first and starts the command only when
// the decision is approved, so a rejected or cancelled call leaves no side
// effect on disk.
func (s *Shell) RunGuarded(
	ctx context.Context,
	gate approval.Gate,
	call GuardedCall,
) (ShellResult, approval.Decision, error) {
	decision, err := gate.Require(ctx, call.Approval)
	if err != nil {
		return ShellResult{}, decision, err
	}
	if !decision.Approved {
		return ShellResult{}, decision, nil
	}
	return s.Execute(ctx, call.Shell), decision, nil
}

// timeout prefers the per-command deadline and falls back to the configuration.
func (s *Shell) timeout(requested time.Duration) time.Duration {
	if requested > noTimeout {
		return requested
	}
	return s.config.Timeout
}

// collect interprets the process outcome and the reason its context ended.
func collect(ctx context.Context, output *limitedWriter, runErr error) ShellResult {
	result := ShellResult{Output: output.String(), Truncated: output.Truncated()}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut, result.Err = true, context.DeadlineExceeded
		return result
	case errors.Is(ctx.Err(), context.Canceled):
		result.Err = context.Canceled
		return result
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		result.ExitCode = exit.ExitCode()
		return result
	}
	result.Err = runErr
	return result
}

// SanitizeEnvironment drops every entry whose key names a credential reference
// while preserving the order of the remaining entries.
func SanitizeEnvironment(environ, secretNames []string) []string {
	blocked := make(map[string]struct{}, len(secretNames))
	for _, name := range secretNames {
		blocked[name] = struct{}{}
	}
	filtered := make([]string, emptyCapacity, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, envAssignment)
		if _, found := blocked[key]; found {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// limitedWriter keeps the first limit bytes written by either stream and
// reports truncation once. It always claims the whole buffer: exec copies from
// a pipe and a short write would surface as io.ErrShortWrite.
type limitedWriter struct {
	mu        sync.Mutex
	buffer    []byte
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - len(w.buffer)
	if remaining >= len(p) {
		w.buffer = append(w.buffer, p...)
		return len(p), nil
	}
	if remaining > emptyCapacity {
		w.buffer = append(w.buffer, p[:remaining]...)
	}
	w.truncated = true
	return len(p), nil
}

// String renders the captured bytes.
func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buffer)
}

// Truncated reports whether output was dropped.
func (w *limitedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

var _ io.Writer = (*limitedWriter)(nil)
