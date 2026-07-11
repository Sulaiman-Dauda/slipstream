package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// Slipstream owns each site user's crontab entirely: the panel renders the
// full crontab from desired state and installs it, so there is one source
// of truth. A managed header marks it.

var crontabSafe = regexp.MustCompile(`^[\x20-\x7e]*$`) // printable ASCII only

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

// siteDataDir is a small helper for adminer/export scratch under a site.
func siteScratchDir(rootPath string) string {
	return filepath.Join(rootPath, "shared", ".slipstream")
}
