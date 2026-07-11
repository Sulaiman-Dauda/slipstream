package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

var dbTableRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// CreateStaging clones production into an isolated staging site: files,
// database, new credentials, rewritten URLs, indexing disabled.
func (a *Agent) CreateStaging(p rpc.StagingParams) (rpc.CreateSiteResult, error) {
	ctx := context.Background()
	prod, stg := p.Production, p.Staging
	if err := validateSite(prod); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	if err := validateSite(stg); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	if err := a.validateSiteRoot(prod); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	if err := a.validateSiteRoot(stg); err != nil {
		return rpc.CreateSiteResult{}, err
	}

	// 1. Provision the staging skeleton (user, dirs, db) without app install.
	stgNoApp := stg
	if prod.Type == state.SiteWordPress || prod.Type == state.SiteWooCommerce {
		stgNoApp.Type = state.SitePHP // skip WordPress bootstrap; we clone instead
	}
	result, err := a.CreateSite(rpc.CreateSiteParams{Site: stgNoApp, DBPassword: p.DBPassword})
	if err != nil {
		return result, err
	}
	complete := false
	defer func() {
		if !complete {
			if _, err := a.DeleteSite(rpc.SiteRef{Site: stg}); err != nil {
				a.Log.Error("staging rollback incomplete", "site", stg.Domain, "err", err)
			}
		}
	}()

	// 2. Clone the current production release into staging's release dir.
	prodCurrent, err := filepath.EvalSymlinks(filepath.Join(prod.RootPath, "current"))
	if err != nil {
		return result, fmt.Errorf("resolve production release: %w", err)
	}
	if !strings.HasPrefix(filepath.Clean(prodCurrent)+string(os.PathSeparator), filepath.Join(prod.RootPath, "releases")+string(os.PathSeparator)) {
		return result, fmt.Errorf("production current release escapes site root")
	}
	stgRelease := filepath.Join(stg.RootPath, "releases", "initial")
	os.RemoveAll(stgRelease)
	if _, err := a.Runner.Run(ctx, "cp", "-a", prodCurrent, stgRelease); err != nil {
		return result, fmt.Errorf("clone files: %w", err)
	}
	// Clone uploads too: staging must not share production's writable data.
	if _, err := a.Runner.Run(ctx, "cp", "-a",
		filepath.Join(prod.RootPath, "shared", "uploads"),
		filepath.Join(stg.RootPath, "shared")); err != nil {
		return result, fmt.Errorf("clone uploads: %w", err)
	}
	uploads := filepath.Join(stgRelease, "wp-content", "uploads")
	if _, err := os.Lstat(uploads); err == nil {
		os.RemoveAll(uploads)
		if err := forceSymlink(filepath.Join(stg.RootPath, "shared", "uploads"), uploads); err != nil {
			return result, err
		}
	}
	// Staging gets its own copy of the shared config: the cloned release's
	// wp-config symlink still points at production's shared file, and the
	// credential rewrites below must never touch production.
	if prodCfg, err := os.ReadFile(filepath.Join(prod.RootPath, "shared", "wp-config.php")); err == nil {
		stgCfg := filepath.Join(stg.RootPath, "shared", "wp-config.php")
		if err := os.WriteFile(stgCfg, prodCfg, 0o640); err != nil {
			return result, err
		}
		os.Remove(filepath.Join(stgRelease, "wp-config.php"))
		if err := forceSymlink(stgCfg, filepath.Join(stgRelease, "wp-config.php")); err != nil {
			return result, err
		}
	}

	// 3. Clone the database.
	if prod.Config.Database.Enabled && !prod.Config.Database.External {
		dumpFile := filepath.Join(a.Paths.WorkDir, fmt.Sprintf("staging-%d.sql", stg.ID))
		if err := os.MkdirAll(a.Paths.WorkDir, 0o700); err != nil {
			return result, err
		}
		defer os.Remove(dumpFile)
		if !dbIdentRe.MatchString(prod.Config.Database.Name) || !dbIdentRe.MatchString(stg.Config.Database.Name) {
			return result, fmt.Errorf("invalid database names")
		}
		if err := a.dumpDatabaseFile(ctx, prod.Config.Database.Name, dumpFile); err != nil {
			return result, fmt.Errorf("dump production db: %w", err)
		}
		if err := a.importDatabaseFile(ctx, stg.Config.Database.Name, dumpFile); err != nil {
			return result, fmt.Errorf("import staging db: %w", err)
		}
	}

	// Cloned files carry production ownership; the staging user must own
	// them before wp-cli (running as that user) can touch wp-config.
	if _, err := a.Runner.Run(ctx, "chown", "-R", stg.SystemUser+":"+stg.SystemUser, stg.RootPath); err != nil {
		return result, err
	}

	// 4. WordPress-specific safety: rewrite URLs, block indexing, update
	// wp-config with staging credentials.
	if prod.Type == state.SiteWordPress || prod.Type == state.SiteWooCommerce {
		wp := func(args ...string) error {
			argv := append([]string{"-u", stg.SystemUser, "--", "wp", "--path=" + stgRelease}, args...)
			_, err := a.Runner.Run(ctx, "runuser", argv...)
			return err
		}
		wpPrompt := func(secret string, args ...string) error {
			argv := append([]string{"-u", stg.SystemUser, "--", "wp", "--path=" + stgRelease}, args...)
			_, err := a.Runner.RunStdin(ctx, secret+"\n", "runuser", argv...)
			return err
		}
		db := stg.Config.Database
		for _, set := range [][]string{
			{"config", "set", "DB_NAME", db.Name},
			{"config", "set", "DB_USER", db.User},
			{"config", "set", "WP_ENVIRONMENT_TYPE", "staging"},
			{"config", "set", "DISALLOW_INDEXING", "true", "--raw"},
		} {
			if err := wp(set...); err != nil {
				return result, fmt.Errorf("staging wp-config: %w", err)
			}
		}
		if err := wpPrompt(p.DBPassword, "config", "set", "DB_PASSWORD", "--prompt=value"); err != nil {
			return result, fmt.Errorf("staging wp-config password: %w", err)
		}
		if p.ConnectorToken != "" {
			if err := wpPrompt(p.ConnectorToken, "config", "set", "SLIPSTREAM_SITE_TOKEN", "--prompt=value"); err != nil {
				return result, fmt.Errorf("staging connector token: %w", err)
			}
		}
		if err := wp("search-replace", "https://"+prod.Domain, "https://"+stg.Domain, "--all-tables", "--precise"); err != nil {
			return result, fmt.Errorf("staging url rewrite: %w", err)
		}
		if err := wp("option", "update", "blog_public", "0"); err != nil {
			return result, fmt.Errorf("staging noindex: %w", err)
		}
	}

	if _, err := a.Runner.Run(ctx, "chown", "-R", stg.SystemUser+":"+stg.SystemUser, stg.RootPath); err != nil {
		return result, err
	}
	if _, err := a.Runner.Run(ctx, "chown", "root:"+stg.SystemUser, stg.RootPath); err != nil {
		return result, err
	}
	if err := os.Chmod(stg.RootPath, 0o750); err != nil {
		return result, err
	}
	files, err := a.renderSite(ctx, stg)
	if err != nil {
		return result, err
	}
	result.Files = files
	if err := a.verifyProvisionedSite(ctx, stg); err != nil {
		return result, err
	}
	complete = true
	return result, nil
}

func protectedLiveTable(site state.Site, table string) bool {
	t := strings.ToLower(table)
	for _, suffix := range []string{"_users", "_usermeta"} {
		if strings.HasSuffix(t, suffix) {
			return true
		}
	}
	if site.Type != state.SiteWooCommerce {
		return false
	}
	for _, suffix := range []string{"_posts", "_postmeta", "_comments", "_commentmeta", "_woocommerce_sessions"} {
		if strings.HasSuffix(t, suffix) {
			return true
		}
	}
	return strings.Contains(t, "_actionscheduler_") || strings.Contains(t, "_wc_order") || strings.Contains(t, "_woocommerce_order")
}

// SyncStagingDatabase replaces only explicitly selected production tables.
// Identity and WooCommerce order/session tables are rejected at this root
// boundary even if a caller bypasses the UI.
func (a *Agent) SyncStagingDatabase(p rpc.SyncStagingDBParams) (map[string]any, error) {
	ctx := context.Background()
	if err := validateSite(p.Production); err != nil {
		return nil, err
	}
	if err := validateSite(p.Staging); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(p.Production); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(p.Staging); err != nil {
		return nil, err
	}
	if p.Staging.StagingOf != p.Production.ID {
		return nil, fmt.Errorf("staging site does not belong to production")
	}
	if len(p.Tables) == 0 || len(p.Tables) > 50 {
		return nil, fmt.Errorf("select between 1 and 50 database tables")
	}
	seen := map[string]bool{}
	for _, table := range p.Tables {
		if !dbTableRe.MatchString(table) || seen[table] {
			return nil, fmt.Errorf("invalid or duplicate table %q", table)
		}
		if protectedLiveTable(p.Production, table) {
			return nil, fmt.Errorf("table %s contains protected live identity, order, or session data", table)
		}
		seen[table] = true
	}
	if !dbIdentRe.MatchString(p.Production.Config.Database.Name) || !dbIdentRe.MatchString(p.Staging.Config.Database.Name) {
		return nil, fmt.Errorf("invalid database names")
	}
	quoted := make([]string, len(p.Tables))
	for i, table := range p.Tables {
		quoted[i] = "'" + table + "'"
	}
	query := "SELECT TABLE_NAME FROM information_schema.tables WHERE table_schema=DATABASE() AND TABLE_NAME IN (" + strings.Join(quoted, ",") + ")"
	out, err := a.Runner.Run(ctx, "mariadb", "--protocol=socket", "--batch", "--skip-column-names", p.Staging.Config.Database.Name, "-e", query)
	if err != nil {
		return nil, fmt.Errorf("verify staging tables: %w", err)
	}
	found := map[string]bool{}
	for _, table := range strings.Fields(out) {
		found[table] = true
	}
	for _, table := range p.Tables {
		if !found[table] {
			return nil, fmt.Errorf("staging table %s not found", table)
		}
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	safety := filepath.Join(a.Paths.WorkDir, "db-push-safety-"+stamp+".sql")
	selected := filepath.Join(a.Paths.WorkDir, "db-push-selected-"+stamp+".sql")
	defer os.Remove(safety)
	defer os.Remove(selected)
	if err := a.dumpDatabaseFile(ctx, p.Production.Config.Database.Name, safety); err != nil {
		return nil, fmt.Errorf("create production database rollback point: %w", err)
	}
	if err := dumpSelectedTables(ctx, p.Staging.Config.Database.Name, p.Tables, selected); err != nil {
		return nil, err
	}
	rollback := func(cause error) (map[string]any, error) {
		if err := a.importDatabaseFile(context.Background(), p.Production.Config.Database.Name, safety); err != nil {
			return nil, fmt.Errorf("%v; database rollback also failed: %w", cause, err)
		}
		return nil, cause
	}
	if err := a.importDatabaseFile(ctx, p.Production.Config.Database.Name, selected); err != nil {
		return rollback(fmt.Errorf("import selected tables: %w", err))
	}
	if p.Production.Type == state.SiteWordPress || p.Production.Type == state.SiteWooCommerce {
		argv := []string{"-u", p.Production.SystemUser, "--", "wp", "--path=" + filepath.Join(p.Production.RootPath, "current"), "search-replace", "https://" + p.Staging.Domain, "https://" + p.Production.Domain}
		argv = append(argv, p.Tables...)
		argv = append(argv, "--precise", "--skip-columns=guid")
		if _, err := a.Runner.Run(ctx, "runuser", argv...); err != nil {
			return rollback(fmt.Errorf("rewrite promoted URLs: %w", err))
		}
	}
	return map[string]any{"tables": p.Tables, "count": len(p.Tables)}, nil
}

func dumpSelectedTables(ctx context.Context, database string, tables []string, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	args := []string{"--protocol=socket", "--single-transaction", database}
	args = append(args, tables...)
	cmd := exec.CommandContext(ctx, "mariadb-dump", args...)
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dump selected staging tables: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return f.Sync()
}
