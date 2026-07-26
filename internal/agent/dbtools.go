package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// DBQuery runs SQL against a database and returns tabular output.
//
// The query does NOT run as the MariaDB superuser. Naming a default database
// does not confine a query to it — `SELECT * FROM otherdb.wp_users` reads
// straight across, so a panel operator scoped to one site could read (and
// write) every other tenant's database through their own console. Instead the
// agent mints a throwaway MariaDB account granted only on this one database
// and runs the statement as that account, letting the server's own grant
// system enforce the boundary. The account is dropped when the query returns.
func (a *Agent) DBQuery(p rpc.DBQueryParams) (rpc.DBQueryResult, error) {
	if !dbIdentRe.MatchString(p.Database) {
		return rpc.DBQueryResult{}, fmt.Errorf("invalid database name")
	}
	if strings.TrimSpace(p.SQL) == "" {
		return rpc.DBQueryResult{}, fmt.Errorf("empty query")
	}
	ctx := context.Background()
	cred, err := a.scopedDBUser(ctx, p.Database)
	if err != nil {
		return rpc.DBQueryResult{}, err
	}
	defer cred.close()

	out, err := a.Runner.Run(ctx, "mariadb", "--defaults-extra-file="+cred.file,
		"--protocol=socket", "--batch", "--column-names", p.Database, "-e", p.SQL)
	if err != nil {
		// Surface MariaDB's own message, not our argv — the raw error embeds
		// the command line including the credential file path, which is both
		// noise for the operator and needless internal detail on screen.
		return rpc.DBQueryResult{Message: databaseErrorMessage(err)}, nil
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	res := rpc.DBQueryResult{}
	if len(lines) == 0 || lines[0] == "" {
		res.Message = "OK (no rows)"
		return res, nil
	}
	res.Columns = strings.Split(lines[0], "\t")
	for _, line := range lines[1:] {
		res.Rows = append(res.Rows, strings.Split(line, "\t"))
	}
	return res, nil
}

// DBExport dumps a database to a timestamped .sql file the user can
// download from the file browser.
func (a *Agent) DBExport(p rpc.DBExportParams) (rpc.DBExportResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.DBExportResult{}, err
	}
	if !dbIdentRe.MatchString(p.Database) {
		return rpc.DBExportResult{}, fmt.Errorf("invalid database name")
	}
	dir := filepath.Join(p.Site.RootPath, "shared", "exports")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return rpc.DBExportResult{}, err
	}
	// MkdirAll runs as the root agent, so a freshly created dir is root:root
	// like every other directory the site user can't traverse -- unlike
	// "uploads" and the rest of "shared", which are chowned at site creation.
	// Without this the exported file (chowned below) is unreachable via the
	// site's own SFTP login even though it displays fine through the panel,
	// which reads it as root.
	a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, dir)
	name := fmt.Sprintf("%s-%s.sql", p.Database, time.Now().UTC().Format("20060102-150405"))
	dest := filepath.Join(dir, name)
	// Stream the dump straight to disk — a multi-GB database must not be
	// buffered (twice) in the agent's memory.
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return rpc.DBExportResult{}, err
	}
	cmd := exec.CommandContext(context.Background(), "mariadb-dump", "--protocol=socket", "--single-transaction", p.Database)
	cmd.Stdout = f
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		f.Close()
		os.Remove(dest)
		return rpc.DBExportResult{}, fmt.Errorf("mariadb-dump: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	f.Close()
	a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, dest)
	fi, err := os.Stat(dest)
	if err != nil {
		return rpc.DBExportResult{}, err
	}
	return rpc.DBExportResult{Path: dest, SizeBytes: fi.Size()}, nil
}

// DBImport replaces a managed database from a SQL file inside the site's
// jailed root. A fresh local dump is restored automatically on any failure.
func (a *Agent) DBImport(p rpc.DBImportParams) (rpc.DBImportResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.DBImportResult{}, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return rpc.DBImportResult{}, err
	}
	if !dbIdentRe.MatchString(p.Database) || p.Database != p.Site.Config.Database.Name {
		return rpc.DBImportResult{}, fmt.Errorf("invalid database name")
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(p.RelPath)), ".sql") {
		return rpc.DBImportResult{}, fmt.Errorf("import file must use the .sql extension")
	}
	path, err := resolveInSite(p.Site.RootPath, p.RelPath)
	if err != nil {
		return rpc.DBImportResult{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return rpc.DBImportResult{}, err
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return rpc.DBImportResult{}, fmt.Errorf("import file must be a non-empty regular file")
	}
	safety := filepath.Join(a.Paths.WorkDir, fmt.Sprintf("db-import-safety-%d.sql", time.Now().UnixNano()))
	defer os.Remove(safety)
	ctx := context.Background()
	if err := a.dumpDatabaseFile(ctx, p.Database, safety); err != nil {
		return rpc.DBImportResult{}, fmt.Errorf("create database rollback point: %w", err)
	}
	if err := a.importDatabaseFile(ctx, p.Database, path); err != nil {
		rollbackErr := a.importDatabaseFile(context.Background(), p.Database, safety)
		if rollbackErr != nil {
			return rpc.DBImportResult{}, fmt.Errorf("database import failed: %v; rollback also failed: %v", err, rollbackErr)
		}
		return rpc.DBImportResult{}, fmt.Errorf("database import failed and was rolled back: %w", err)
	}
	return rpc.DBImportResult{Imported: p.RelPath, SizeBytes: fi.Size()}, nil
}

// adminerWrapper is the auto-login shim placed next to the cached Adminer.
// It fills the site's credentials, enforces an expiry, and self-destructs.
const adminerWrapperTmpl = `<?php
// Managed by Slipstream — time-limited database console. Self-destructs.
if (time() > %d) {
    @unlink(__FILE__);
    @unlink(__DIR__ . '/%s');
    http_response_code(410);
    exit('This database session has expired. Launch a new one from the panel.');
}
// Pre-seed Adminer's own session so it connects with the managed credentials
// and never shows a login form — the operator never has to know (or type) the
// database password. Adminer's auth gate reads the password from
// $_SESSION['pwds'][driver][server][username]; credentials() below supplies
// the actual connection, but the gate must find this entry first or Adminer
// falls back to the login screen.
session_name('adminer_sid');
@session_start();
$_SESSION['pwds']['server']['127.0.0.1'][%s] = %s;
session_write_close();
function adminer_object() {
    class SlipstreamAdminer extends Adminer {
        function credentials() { return ['127.0.0.1', %s, %s]; }
        function database() { return %s; }
        function databases($flush = true) { return [%s]; }
        function login($login, $password) { return true; }
    }
    return new SlipstreamAdminer;
}
$_GET['server'] = '127.0.0.1';
$_GET['username'] = %s;
$_GET['db'] = %s;
include __DIR__ . '/%s';
`

// LaunchAdminer caches Adminer (fetching it once) and drops a tokenized,
// expiring auto-login wrapper into the site's document root.
func (a *Agent) LaunchAdminer(p rpc.AdminerParams) (rpc.AdminerResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.AdminerResult{}, err
	}
	if !adminerTokenRe.MatchString(p.Token) {
		return rpc.AdminerResult{}, fmt.Errorf("invalid token")
	}
	if !dbIdentRe.MatchString(p.DBName) || !dbIdentRe.MatchString(p.DBUser) {
		return rpc.AdminerResult{}, fmt.Errorf("invalid database identifiers")
	}

	// Cache the Adminer single-file app.
	cache := filepath.Join(a.Paths.WorkDir, "adminer.php")
	if _, err := os.Stat(cache); os.IsNotExist(err) {
		os.MkdirAll(a.Paths.WorkDir, 0o755)
		if _, err := a.Runner.Run(context.Background(), "curl", "-fsSL", "-o", cache,
			"https://github.com/vrana/adminer/releases/download/v4.8.1/adminer-4.8.1.php"); err != nil {
			return rpc.AdminerResult{}, fmt.Errorf("could not download Adminer: %w", err)
		}
	}

	docroot := filepath.Join(p.Site.RootPath, "current")
	// The real Adminer stays a dotfile so nginx's dotfile deny rule blocks
	// direct download of the credential-bearing include; only the wrapper
	// (non-dot, token in the name) is web-reachable, and it self-destructs.
	realName := ".slip-adminer-" + p.Token + ".php"
	wrapName := "slip-db-" + p.Token + ".php"
	if err := copyFile(cache, filepath.Join(docroot, realName), p.Site.SystemUser, a); err != nil {
		return rpc.AdminerResult{}, err
	}
	wrapper := fmt.Sprintf(adminerWrapperTmpl,
		p.ExpiryUnix, realName,
		phpStr(p.DBUser), phpStr(p.DBPassword),
		phpStr(p.DBUser), phpStr(p.DBPassword), phpStr(p.DBName), phpStr(p.DBName),
		phpStr(p.DBUser), phpStr(p.DBName), realName)
	wrapPath := filepath.Join(docroot, wrapName)
	if err := os.WriteFile(wrapPath, []byte(wrapper), 0o640); err != nil {
		return rpc.AdminerResult{}, err
	}
	a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, wrapPath)

	// The Adminer session cleanup (removing the token files after expiry) is
	// handled by the wrapper's self-destruct on next access; a belt-and-
	// braces sweep also runs from the scheduler.
	return rpc.AdminerResult{URL: fmt.Sprintf("https://%s/%s", p.Site.Domain, wrapName)}, nil
}

// phpStr renders a Go string as a single-quoted PHP literal.
func phpStr(s string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'"
}

func copyFile(src, dst, owner string, a *Agent) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o640); err != nil {
		return err
	}
	if owner != "" {
		a.Runner.Run(context.Background(), "chown", owner+":"+owner, dst)
	}
	return nil
}

// scopedCred is a short-lived MariaDB account confined to one database, plus
// the defaults-file holding its password. The password never appears in argv
// (which is world-readable via /proc), only in a 0600 file owned by root.
type scopedCred struct {
	user  string
	file  string
	close func()
}

// scopedDBUser creates a throwaway MariaDB account with privileges on exactly
// one database and returns credentials for it. Call close() when done — it
// drops the account and removes the defaults-file.
//
// This exists so the SQL console cannot reach across databases. Running console
// SQL as root meant a site-scoped operator could read and write every other
// tenant's data by qualifying the table name; MariaDB's grants are the only
// reliable place to enforce that boundary, because inspecting the SQL for
// cross-schema references is not something a prefix check can do safely.
func (a *Agent) scopedDBUser(ctx context.Context, database string) (scopedCred, error) {
	if !dbIdentRe.MatchString(database) {
		return scopedCred{}, fmt.Errorf("invalid database name")
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return scopedCred{}, err
	}
	pwBytes := make([]byte, 24)
	if _, err := rand.Read(pwBytes); err != nil {
		return scopedCred{}, err
	}
	// Usernames are capped at 80 chars in MariaDB; this is well under.
	user := "slipq_" + hex.EncodeToString(suffix)
	password := base64.RawURLEncoding.EncodeToString(pwBytes)

	// The identifiers here are ours (a hex suffix and a validated database
	// name), never user input, so interpolation is safe. The password is
	// base64url — no quote characters — and is passed on stdin, not argv.
	stmt := fmt.Sprintf(
		"CREATE USER '%s'@'localhost' IDENTIFIED BY '%s' WITH MAX_USER_CONNECTIONS 4;\n"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\nFLUSH PRIVILEGES;\n",
		user, password, database, user)
	if _, err := a.Runner.RunStdin(ctx, stmt, "mariadb", "--protocol=socket"); err != nil {
		return scopedCred{}, fmt.Errorf("create scoped database user: %w", err)
	}

	drop := func() {
		_, _ = a.Runner.RunStdin(context.Background(),
			fmt.Sprintf("DROP USER IF EXISTS '%s'@'localhost';\n", user),
			"mariadb", "--protocol=socket")
	}

	f, err := os.CreateTemp(a.Paths.WorkDir, ".dbq-*.cnf")
	if err != nil {
		// Fall back to the system temp dir if the work dir is unavailable.
		f, err = os.CreateTemp("", ".slipstream-dbq-*.cnf")
		if err != nil {
			drop()
			return scopedCred{}, err
		}
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		drop()
		return scopedCred{}, err
	}
	if _, err := fmt.Fprintf(f, "[client]\nuser=%s\npassword=%s\n", user, password); err != nil {
		f.Close()
		os.Remove(f.Name())
		drop()
		return scopedCred{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		drop()
		return scopedCred{}, err
	}

	name := f.Name()
	return scopedCred{user: user, file: name, close: func() {
		os.Remove(name)
		drop()
	}}, nil
}

// databaseErrorMessage extracts the database server's own error from a Runner
// failure. Runner formats errors as "<cmd> <args>: <err>: <stderr>", which for
// the SQL console would print the whole mariadb invocation — including the
// temporary credential file — back to the operator.
func databaseErrorMessage(err error) string {
	msg := err.Error()
	// Prefer the ERROR line MariaDB emits; it is the part that is useful.
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERROR ") {
			return line
		}
	}
	// Otherwise take everything after the last ": " that Runner prepended.
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		if tail := strings.TrimSpace(msg[i+2:]); tail != "" {
			return tail
		}
	}
	return "query failed"
}
