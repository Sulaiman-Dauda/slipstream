package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// wp runs a wp-cli command as the site's Unix user against its current
// release.
func (a *Agent) wp(ctx context.Context, site state.Site, args ...string) (string, error) {
	release := filepath.Join(site.RootPath, "current")
	argv := append([]string{"-u", site.SystemUser, "--", "wp", "--path=" + release}, args...)
	return a.Runner.Run(ctx, "runuser", argv...)
}

// WPMagicLogin installs a one-time-login helper and returns a URL that logs
// the admin straight into wp-admin. Uses the well-maintained wp-cli
// "login" package if present; otherwise creates a short-lived auth cookie
// via a generated magic link through a tiny mu-plugin.
func (a *Agent) WPMagicLogin(p rpc.WPParams) (rpc.WPLoginResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.WPLoginResult{}, err
	}
	ctx := context.Background()
	// Ensure the wp-cli login command package is available (installs once).
	if _, err := a.wp(ctx, p.Site, "login", "--help"); err != nil {
		if _, err := a.Runner.Run(ctx, "runuser", "-u", p.Site.SystemUser, "--",
			"wp", "--path="+filepath.Join(p.Site.RootPath, "current"),
			"package", "install", "aaemnnosttv/wp-cli-login-command"); err != nil {
			return rpc.WPLoginResult{}, fmt.Errorf("install login command: %w", err)
		}
	}
	// Install + activate the companion mu-plugin that serves magic links.
	if _, err := a.wp(ctx, p.Site, "login", "install", "--activate", "--yes"); err != nil {
		return rpc.WPLoginResult{}, fmt.Errorf("activate login companion: %w", err)
	}

	// Find the admin user (lowest ID with administrator role).
	adminUser, err := a.wp(ctx, p.Site, "user", "list", "--role=administrator", "--field=user_login", "--number=1")
	if err != nil || strings.TrimSpace(adminUser) == "" {
		return rpc.WPLoginResult{}, fmt.Errorf("no administrator user found")
	}
	out, err := a.wp(ctx, p.Site, "login", "create", strings.TrimSpace(adminUser), "--url-only")
	if err != nil {
		return rpc.WPLoginResult{}, fmt.Errorf("create magic link: %w", err)
	}
	return rpc.WPLoginResult{URL: strings.TrimSpace(out)}, nil
}

// WPPlugins lists installed plugins and themes with available updates.
func (a *Agent) WPPlugins(p rpc.WPParams) (rpc.WPPluginsResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.WPPluginsResult{}, err
	}
	ctx := context.Background()
	res := rpc.WPPluginsResult{}

	parse := func(raw string) []rpc.WPPlugin {
		var items []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Version string `json:"version"`
			Update  string `json:"update"`
		}
		json.Unmarshal([]byte(raw), &items)
		out := make([]rpc.WPPlugin, 0, len(items))
		for _, it := range items {
			out = append(out, rpc.WPPlugin{Name: it.Name, Status: it.Status, Version: it.Version, Update: it.Update})
		}
		return out
	}

	if out, err := a.wp(ctx, p.Site, "plugin", "list", "--format=json"); err == nil {
		res.Plugins = parse(out)
	}
	if out, err := a.wp(ctx, p.Site, "theme", "list", "--format=json"); err == nil {
		res.Themes = parse(out)
	}
	return res, nil
}

// WPUpdate updates core, plugins, or themes.
func (a *Agent) WPUpdate(p rpc.WPParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	ctx := context.Background()
	switch p.What {
	case "core":
		if _, err := a.wp(ctx, p.Site, "core", "update"); err != nil {
			return nil, err
		}
		a.wp(ctx, p.Site, "core", "update-db")
	case "plugins":
		if _, err := a.wp(ctx, p.Site, "plugin", "update", "--all"); err != nil {
			return nil, err
		}
	case "themes":
		if _, err := a.wp(ctx, p.Site, "theme", "update", "--all"); err != nil {
			return nil, err
		}
	case "all":
		a.wp(ctx, p.Site, "core", "update")
		a.wp(ctx, p.Site, "core", "update-db")
		a.wp(ctx, p.Site, "plugin", "update", "--all")
		a.wp(ctx, p.Site, "theme", "update", "--all")
	default:
		return nil, fmt.Errorf("unknown update target %q", p.What)
	}
	return map[string]string{"updated": p.What}, nil
}

// WPObjectCache enables or disables Redis object caching for a WordPress
// site: installs redis-cache plugin, points it at the site's namespace, and
// enables the drop-in. Assumes Redis is installed (the API ensures this).
func (a *Agent) WPObjectCache(p rpc.WPParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	ctx := context.Background()
	if !p.Enable {
		a.wp(ctx, p.Site, "redis", "disable")
		a.wp(ctx, p.Site, "plugin", "deactivate", "redis-cache")
		return map[string]string{"object_cache": "disabled"}, nil
	}
	// Unique key prefix so sites never collide in a shared Redis.
	prefix := fmt.Sprintf("slip%d:", p.Site.ID)
	a.wp(ctx, p.Site, "config", "set", "WP_REDIS_HOST", "127.0.0.1")
	a.wp(ctx, p.Site, "config", "set", "WP_REDIS_PREFIX", prefix)
	a.wp(ctx, p.Site, "config", "set", "WP_REDIS_MAXTTL", "86400", "--raw")
	if _, err := a.wp(ctx, p.Site, "plugin", "install", "redis-cache", "--activate"); err != nil {
		return nil, fmt.Errorf("install redis-cache: %w", err)
	}
	if _, err := a.wp(ctx, p.Site, "redis", "enable"); err != nil {
		return nil, fmt.Errorf("enable object cache: %w", err)
	}
	return map[string]string{"object_cache": "enabled", "prefix": prefix}, nil
}
