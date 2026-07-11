package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

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

	// 1. Provision the staging skeleton (user, dirs, db) without app install.
	stgNoApp := stg
	stgNoApp.Type = state.SitePHP // skip WordPress bootstrap; we clone instead
	result, err := a.CreateSite(rpc.CreateSiteParams{Site: stgNoApp, AdminPassword: p.DBPassword})
	if err != nil {
		return result, err
	}

	// 2. Clone the current production release into staging's release dir.
	prodCurrent, err := os.Readlink(filepath.Join(prod.RootPath, "current"))
	if err != nil {
		return result, fmt.Errorf("resolve production release: %w", err)
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
		out, err := a.Runner.Run(ctx, "mariadb-dump", "--protocol=socket", "--single-transaction", prod.Config.Database.Name)
		if err != nil {
			return result, fmt.Errorf("dump production db: %w", err)
		}
		if err := os.WriteFile(dumpFile, []byte(out), 0o600); err != nil {
			return result, err
		}
		if _, err := a.Runner.Run(ctx, "mariadb", "--protocol=socket", stg.Config.Database.Name, "-e", "source "+dumpFile); err != nil {
			return result, fmt.Errorf("import staging db: %w", err)
		}
	}

	// 4. WordPress-specific safety: rewrite URLs, block indexing, update
	// wp-config with staging credentials.
	if prod.Type == state.SiteWordPress || prod.Type == state.SiteWooCommerce {
		wp := func(args ...string) error {
			argv := append([]string{"-u", stg.SystemUser, "--", "wp", "--path=" + stgRelease}, args...)
			_, err := a.Runner.Run(ctx, "runuser", argv...)
			return err
		}
		db := stg.Config.Database
		for _, set := range [][]string{
			{"config", "set", "DB_NAME", db.Name},
			{"config", "set", "DB_USER", db.User},
			{"config", "set", "DB_PASSWORD", p.DBPassword},
			{"config", "set", "WP_ENVIRONMENT_TYPE", "staging"},
			{"config", "set", "DISALLOW_INDEXING", "true", "--raw"},
		} {
			if err := wp(set...); err != nil {
				return result, fmt.Errorf("staging wp-config: %w", err)
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
	return result, nil
}
