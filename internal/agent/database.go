package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/sysprobe"
	"github.com/slipstream-panel/slipstream/internal/tune"
)

// waitForMariaDB blocks until the server accepts a socket connection, or the
// budget expires. `systemctl restart` returns when systemd is satisfied, which
// is earlier than MariaDB accepting connections — issuing SQL in that window
// fails with "ERROR 2002 (HY000): Can't connect to local server".
//
// Observed in the wild: provisioning four sites at once on a fresh install, the
// first site with a database triggered the tuning restart below and a concurrent
// site's CREATE DATABASE landed during it, failing that provision outright.
func (a *Agent) waitForMariaDB(ctx context.Context, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		if _, lastErr = a.Runner.Run(ctx, "mariadb", "--protocol=socket", "-e", "SELECT 1"); lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mariadb did not accept connections within %s: %w", budget, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// tuneMariaDBIfNeeded applies hardware-aware MariaDB tuning the first time a
// database is created. It sizes the InnoDB buffer pool and connection limits
// from measured RAM/CPU (shared-server role), writes the managed config,
// restarts MariaDB and waits for it to come back. Idempotent: it no-ops once
// the config exists so operator changes and re-tunes are never clobbered.
//
// Holding dbMu for the whole function is what makes concurrent provisioning
// safe: callers that arrive while the restart is in flight block here rather
// than issuing SQL at a server that is still starting.
func (a *Agent) tuneMariaDBIfNeeded(ctx context.Context) {
	a.dbMu.Lock()
	defer a.dbMu.Unlock()
	if _, err := os.Stat(tune.ConfigPath); err == nil {
		return // already tuned
	}
	facts, err := sysprobe.Probe(a.Paths.SitesRoot)
	if err != nil {
		a.Log.Warn("mariadb tune: probe failed", "err", err)
		return
	}
	cfg := tune.CalculateMariaDB(facts.MemTotalMB, facts.CPUCount, tune.RoleShared, false)
	content, err := cfg.Render()
	if err != nil {
		return
	}
	if _, err := writeManaged(tune.ConfigPath, content, 0o644); err != nil {
		a.Log.Warn("mariadb tune: write failed", "err", err)
		return
	}
	if _, err := a.Runner.Run(ctx, "systemctl", "restart", "mariadb"); err != nil {
		a.Log.Warn("mariadb tune: restart failed", "err", err)
		return
	}
	if err := a.waitForMariaDB(ctx, 60*time.Second); err != nil {
		a.Log.Warn("mariadb tune: server did not come back", "err", err)
		return
	}
	a.Log.Info("mariadb tuned for hardware", "config", cfg.String())
}

// dbIdentRe is the security boundary for SQL identifiers: names generated
// by the API always match, and anything else is rejected before it reaches
// a query.
var dbIdentRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

// mysqlExec runs a statement as the local root MariaDB account (unix-socket
// auth). Identifiers are validated; values are quoted literals with quote
// characters rejected by validation.
func (a *Agent) mysqlExec(ctx context.Context, stmt string) error {
	_, err := a.Runner.Run(ctx, "mariadb", "--protocol=socket", "-e", stmt)
	return err
}

// mysqlExecStdin runs SQL fed on stdin instead of argv, so any embedded
// secret (a new user's password) never appears in /proc/<pid>/cmdline where
// a co-hosted tenant could read it.
func (a *Agent) mysqlExecStdin(ctx context.Context, sql string) error {
	_, err := a.Runner.RunStdin(ctx, sql, "mariadb", "--protocol=socket")
	return err
}

// CreateDatabase creates a database and a dedicated user with access to it
// alone. Passwords never touch a shell: the statement is passed as one argv
// element.
func (a *Agent) CreateDatabase(p rpc.DatabaseParams) (map[string]string, error) {
	ctx := context.Background()
	if !dbIdentRe.MatchString(p.Name) || !dbIdentRe.MatchString(p.User) {
		return nil, fmt.Errorf("invalid database identifiers %q/%q", p.Name, p.User)
	}
	if len(p.Password) < 16 {
		return nil, fmt.Errorf("database password too short")
	}
	for _, c := range p.Password {
		if c == '\'' || c == '\\' || c == '"' || c == '`' {
			return nil, fmt.Errorf("database password contains forbidden characters")
		}
	}
	maxConns := p.MaxConns
	if maxConns <= 0 {
		maxConns = 50
	}
	// Tune MariaDB for this machine on first database creation.
	a.tuneMariaDBIfNeeded(ctx)
	// The password appears only in this SQL, fed on stdin — never in argv.
	sql := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
			"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s' WITH MAX_USER_CONNECTIONS %d;\n"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\nFLUSH PRIVILEGES;\n",
		p.Name, p.User, p.Password, maxConns, p.Name, p.User)
	if err := a.mysqlExecStdin(ctx, sql); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	return map[string]string{"database": p.Name, "user": p.User}, nil
}

// DropDatabase removes a site's database and user.
func (a *Agent) DropDatabase(p rpc.DatabaseParams) (map[string]string, error) {
	ctx := context.Background()
	if !dbIdentRe.MatchString(p.Name) || !dbIdentRe.MatchString(p.User) {
		return nil, fmt.Errorf("invalid database identifiers %q/%q", p.Name, p.User)
	}
	stmts := []string{
		fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;", p.Name),
		fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost';", p.User),
		"FLUSH PRIVILEGES;",
	}
	for _, s := range stmts {
		if err := a.mysqlExec(ctx, s); err != nil {
			return nil, fmt.Errorf("drop database: %w", err)
		}
	}
	return map[string]string{"dropped": p.Name}, nil
}
