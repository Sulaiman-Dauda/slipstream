package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// Slipstream owns each site user's crontab entirely: the panel renders the
// full crontab from desired state and installs it, so there is one source
// of truth. A managed header marks it.

var crontabSafe = regexp.MustCompile(`^[\x20-\x7e]*$`) // printable ASCII only

type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		copy(w.buf, w.buf[len(w.buf)-w.max:])
		w.buf = w.buf[:w.max]
	}
	return n, nil
}

// WriteCrontab replaces a site user's crontab with panel-rendered content.
func (a *Agent) WriteCrontab(p rpc.CrontabParams) (map[string]string, error) {
	if !systemUserRe.MatchString(p.SystemUser) {
		return nil, fmt.Errorf("invalid system user %q", p.SystemUser)
	}
	for _, line := range splitLines(p.Content) {
		if !crontabSafe.MatchString(line) {
			return nil, fmt.Errorf("crontab contains non-printable characters")
		}
	}
	// Write to a temp file, then install with `crontab -u <user> <file>`.
	tmp, err := os.CreateTemp(a.Paths.WorkDir, "crontab-*")
	if err != nil {
		if os.IsNotExist(err) {
			os.MkdirAll(a.Paths.WorkDir, 0o700)
			tmp, err = os.CreateTemp(a.Paths.WorkDir, "crontab-*")
		}
		if err != nil {
			return nil, err
		}
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(p.Content); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	if p.Content == "" {
		// Empty content means remove the crontab.
		a.Runner.Run(context.Background(), "crontab", "-u", p.SystemUser, "-r")
		return map[string]string{"crontab": "cleared"}, nil
	}
	if _, err := a.Runner.Run(context.Background(), "crontab", "-u", p.SystemUser, tmp.Name()); err != nil {
		return nil, fmt.Errorf("install crontab: %w", err)
	}
	return map[string]string{"crontab": "installed"}, nil
}

// RunCron executes one job on demand with the same unprivileged identity as
// cron. It is time-bounded and returns only the final 64 KiB of output.
func (a *Agent) RunCron(p rpc.RunCronParams) (rpc.RunCronResult, error) {
	if !systemUserRe.MatchString(p.SystemUser) || strings.TrimSpace(p.Command) == "" || strings.ContainsAny(p.Command, "\r\n") {
		return rpc.RunCronResult{}, fmt.Errorf("invalid cron command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// -l (login) makes runuser chdir to the site user's home (the site root)
	// and set up its environment the same way crond does when it actually
	// runs this job on schedule. Without it, "Run Now" executes with
	// whatever cwd the agent process happens to have -- so any command using
	// a site-relative path (e.g. "php current/artisan schedule:run") fails
	// here even though the real scheduled run succeeds, misleading the
	// operator into thinking a perfectly working cron job is broken.
	cmd := exec.CommandContext(ctx, "runuser", "-l", p.SystemUser, "-s", "/bin/sh", "-c", p.Command)
	output := tailWriter{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	text := string(output.buf)
	if ctx.Err() == context.DeadlineExceeded {
		return rpc.RunCronResult{Status: "timeout", Output: text}, nil
	}
	if err != nil {
		return rpc.RunCronResult{Status: "failed", Output: text}, nil
	}
	return rpc.RunCronResult{Status: "succeeded", Output: text}, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
