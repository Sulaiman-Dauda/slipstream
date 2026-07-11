package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// DBQuery runs SQL against a database and returns tabular output. The API
// restricts which statements are allowed; the agent enforces the database
// name is well-formed and runs through mariadb with tab-separated output.
func (a *Agent) DBQuery(p rpc.DBQueryParams) (rpc.DBQueryResult, error) {
	if !dbIdentRe.MatchString(p.Database) {
		return rpc.DBQueryResult{}, fmt.Errorf("invalid database name")
	}
	if strings.TrimSpace(p.SQL) == "" {
		return rpc.DBQueryResult{}, fmt.Errorf("empty query")
	}
	out, err := a.Runner.Run(context.Background(), "mariadb", "--protocol=socket",
		"--batch", "--column-names", p.Database, "-e", p.SQL)
	if err != nil {
		return rpc.DBQueryResult{Message: err.Error()}, nil
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
	name := fmt.Sprintf("%s-%s.sql", p.Database, time.Now().UTC().Format("20060102-150405"))
	dest := filepath.Join(dir, name)
	out, err := a.Runner.Run(context.Background(), "mariadb-dump", "--protocol=socket", "--single-transaction", p.Database)
	if err != nil {
		return rpc.DBExportResult{}, err
	}
	if err := os.WriteFile(dest, []byte(out), 0o640); err != nil {
		return rpc.DBExportResult{}, err
	}
	a.Runner.Run(context.Background(), "chown", p.Site.SystemUser+":"+p.Site.SystemUser, dest)
	fi, _ := os.Stat(dest)
	return rpc.DBExportResult{Path: dest, SizeBytes: fi.Size()}, nil
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
function adminer_object() {
    class SlipstreamAdminer extends Adminer {
        function credentials() { return ['127.0.0.1', %s, %s]; }
        function database() { return %s; }
        function databases($flush = true) { return [%s]; }
        function login($login, $password) { return true; }
        function permanentLogin($create = false) { return %s; }
    }
    return new SlipstreamAdminer;
}
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
		phpStr(p.DBUser), phpStr(p.DBPassword), phpStr(p.DBName), phpStr(p.DBName), "false",
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
