package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// resolveLogPath maps a logical log source to a concrete path. Site logs
// live under the panel's per-site log dir; system logs are fixed. The
// domain is validated to stop path traversal.
func (a *Agent) resolveLogPath(source, site string) (string, error) {
	switch source {
	case "nginx-access":
		return "/var/log/nginx/access.log", nil
	case "nginx-error":
		return "/var/log/nginx/error.log", nil
	case "mariadb":
		return "/var/log/mysql/error.log", nil
	}
	if strings.HasPrefix(source, "site:") {
		if err := nginx.ValidateDomain(site); err != nil {
			return "", err
		}
		dir := filepath.Join(a.Paths.LogRoot, site)
		switch {
		case strings.HasSuffix(source, ":access"):
			return filepath.Join(dir, "access.log"), nil
		case strings.HasSuffix(source, ":error"):
			return filepath.Join(dir, "error.log"), nil
		case strings.HasSuffix(source, ":php"):
			return filepath.Join(dir, "php-error.log"), nil
		}
	}
	return "", fmt.Errorf("unknown log source %q", source)
}

// TailLog returns the last N lines of a known log via journald (services)
// or file tail (access/error logs).
func (a *Agent) TailLog(p rpc.TailLogParams) (rpc.TailLogResult, error) {
	lines := p.Lines
	if lines <= 0 || lines > 2000 {
		lines = 200
	}

	// Service logs come from journald, not files.
	if p.Source == "agent" || p.Source == "api" {
		unit := "slipstream-agent"
		if p.Source == "api" {
			unit = "slipstream-api"
		}
		out, err := a.Runner.Run(context.Background(), "journalctl", "-u", unit,
			"-n", fmt.Sprintf("%d", lines), "--no-pager", "-o", "short-iso")
		if err != nil {
			return rpc.TailLogResult{}, err
		}
		return rpc.TailLogResult{Path: "journal:" + unit, Content: out}, nil
	}
	if p.Source == "php-error" {
		// PHP-FPM's own error log; per-site php errors use site:<d>:php.
		out, _ := a.Runner.Run(context.Background(), "journalctl", "-u", a.phpFPMUnit(),
			"-n", fmt.Sprintf("%d", lines), "--no-pager", "-o", "short-iso")
		return rpc.TailLogResult{Path: "journal:" + a.phpFPMUnit(), Content: out}, nil
	}

	path, err := a.resolveLogPath(p.Source, p.Site)
	if err != nil {
		return rpc.TailLogResult{}, err
	}
	content, err := tailFile(path, lines)
	if err != nil {
		return rpc.TailLogResult{}, err
	}
	return rpc.TailLogResult{Path: path, Content: content}, nil
}

// tailFile returns the last n lines of a file without loading all of it.
func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "(log is empty or not created yet)", nil
		}
		return "", err
	}
	defer f.Close()

	// Ring buffer of the last n lines.
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	return strings.Join(ring, "\n"), sc.Err()
}
