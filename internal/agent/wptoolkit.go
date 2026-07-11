package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// dropinPath returns the object-cache.php path in a site's live release.
func dropinPath(site state.Site) string {
	return filepath.Join(site.RootPath, "current", "wp-content", "object-cache.php")
}

// WPObjectCache enables or disables persistent object caching. The backend
// is chosen by topology: single server uses the APCu drop-in (in-process,
// no daemon); Redis is used when p.What == "redis" (scale mode / explicit).
func (a *Agent) WPObjectCache(p rpc.WPParams) (map[string]string, error) {
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	ctx := context.Background()
	dropin := dropinPath(p.Site)

	if !p.Enable {
		os.Remove(dropin)
		a.wp(ctx, p.Site, "redis", "disable")
		a.wp(ctx, p.Site, "plugin", "deactivate", "redis-cache")
		a.reloadPHPFPM(ctx, p.Site.PHPVersion)
		return map[string]string{"object_cache": "disabled"}, nil
	}

	if p.What == "redis" {
		prefix := fmt.Sprintf("slip%d:", p.Site.ID)
		a.wp(ctx, p.Site, "config", "set", "WP_REDIS_HOST", "127.0.0.1")
		a.wp(ctx, p.Site, "config", "set", "WP_REDIS_PREFIX", prefix)
		a.wp(ctx, p.Site, "config", "set", "WP_REDIS_MAXTTL", "86400", "--raw")
		if _, err := a.wp(ctx, p.Site, "plugin", "install", "redis-cache", "--activate"); err != nil {
			return nil, fmt.Errorf("install redis-cache: %w", err)
		}
		if _, err := a.wp(ctx, p.Site, "redis", "enable"); err != nil {
			return nil, fmt.Errorf("enable redis object cache: %w", err)
		}
		return map[string]string{"object_cache": "enabled", "backend": "redis", "prefix": prefix}, nil
	}

	// Default: APCu drop-in. Written to shared/ and symlinked so it survives
	// deploys (like wp-config), then chowned to the site user.
	shared := filepath.Join(p.Site.RootPath, "shared", "object-cache.php")
	if err := os.WriteFile(shared, []byte(APCuDropin), 0o640); err != nil {
		return nil, fmt.Errorf("write drop-in: %w", err)
	}
	os.Remove(dropin)
	if err := forceSymlink(shared, dropin); err != nil {
		return nil, err
	}
	a.Runner.Run(ctx, "chown", "-h", p.Site.SystemUser+":"+p.Site.SystemUser, dropin)
	a.Runner.Run(ctx, "chown", p.Site.SystemUser+":"+p.Site.SystemUser, shared)
	// Reload FPM so OPcache picks up the new/removed drop-in immediately —
	// otherwise a stale cached object-cache.php can fatal the site.
	a.reloadPHPFPM(ctx, p.Site.PHPVersion)
	return map[string]string{"object_cache": "enabled", "backend": "apcu"}, nil
}

// CacheStats returns object-cache hit/miss/memory numbers for observability.
func (a *Agent) CacheStats(p rpc.WPParams) (rpc.CacheStatsResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.CacheStatsResult{}, err
	}
	ctx := context.Background()
	res := rpc.CacheStatsResult{Backend: "none"}

	// Which backend is active?
	if _, err := os.Lstat(dropinPath(p.Site)); err == nil {
		res.Backend = "apcu"
		// Read APCu stats via a tiny PHP program run as the site user.
		out, err := a.Runner.Run(ctx, "runuser", "-u", p.Site.SystemUser, "--",
			"php", "-r", "$i=apcu_cache_info(true);$s=apcu_sma_info(true);echo json_encode(['hits'=>$i['num_hits']??0,'misses'=>$i['num_misses']??0,'entries'=>$i['num_entries']??0,'mem_used'=>($s['seg_size']??0)-($s['avail_mem']??0),'mem_total'=>$s['seg_size']??0]);")
		if err == nil {
			json.Unmarshal([]byte(out), &res)
		}
	} else if out, err := a.wp(ctx, p.Site, "redis", "status"); err == nil && strings.Contains(out, "Status: Connected") {
		res.Backend = "redis"
	}
	if res.HitsPlusMisses() > 0 {
		res.HitRatePct = int(float64(res.Hits) * 100 / float64(res.HitsPlusMisses()))
	}
	return res, nil
}
