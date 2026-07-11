package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/sysprobe"
	"github.com/slipstream-panel/slipstream/internal/tune"
)

// tuneMariaDBIfNeeded applies hardware-aware MariaDB tuning the first time a
// database is created. It sizes the InnoDB buffer pool and connection limits
// from measured RAM/CPU (shared-server role), writes the managed config, and
// restarts MariaDB. Idempotent: it no-ops once the config exists so operator
// changes and re-tunes are never clobbered.
func (a *Agent) tuneMariaDBIfNeeded(ctx context.Context) {
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
	a.Log.Info("mariadb tuned for hardware", "config", cfg.String())
}

// dbIdentRe is the security boundary for SQL identifiers: names generated
// by the API always match, and anything else is rejected before it reaches
// a query.
var dbIdentRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,31}$`)

// mysqlExec runs a statement as the local root MariaDB account (unix-socket
// auth). Identifiers are validated; values are passed as quoted literals
// with all quotes stripped by validation.
func (a *Agent) mysqlExec(ctx context.Context, stmt string) error {
	_, err := a.Runner.Run(ctx, "mariadb", "--protocol=socket", "-e", stmt)
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
	stmts := []string{
		fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", p.Name),
		fmt.Sprintf("CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s' WITH MAX_USER_CONNECTIONS %d;", p.User, p.Password, maxConns),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';", p.Name, p.User),
		"FLUSH PRIVILEGES;",
	}
	for _, s := range stmts {
		if err := a.mysqlExec(ctx, s); err != nil {
			return nil, fmt.Errorf("create database: %w", err)
		}
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
