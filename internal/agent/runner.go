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
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
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
