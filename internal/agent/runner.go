package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes system commands. Commands are always argv arrays — the
// agent never builds shell strings, which is the boundary that keeps
// user-supplied names (domains, database names) from becoming injection.
// RunStdin additionally feeds data on stdin, used to keep secrets (DB and
// admin passwords) out of argv / /proc/<pid>/cmdline.
// RunCombined returns stdout and stderr together, for the handful of tools
// that report on stderr even when they succeed (`nginx -V` prints its whole
// configure line there and exits 0). Run would return an empty string for
// those, which silently reads as "feature absent".
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
	RunStdin(ctx context.Context, stdin string, name string, args ...string) (stdout string, err error)
	RunCombined(ctx context.Context, name string, args ...string) (output string, err error)
}

// ExecRunner runs real commands with a hard timeout.
type ExecRunner struct {
	Timeout time.Duration
}

// Run executes name with args and returns trimmed stdout. Stderr is folded
// into the error for diagnostics.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RunStdin executes name with args, feeding stdin, returning trimmed stdout.
// RunCombined executes name with args and returns trimmed stdout+stderr.
func (r ExecRunner) RunCombined(ctx context.Context, name string, args ...string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(buf.String()), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func (r ExecRunner) RunStdin(ctx context.Context, stdin, name string, args ...string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
