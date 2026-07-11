package agent

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/sysprobe"
	"github.com/slipstream-panel/slipstream/internal/velocity"
	"github.com/slipstream-panel/slipstream/internal/version"
)

func (a *Agent) serviceActive(ctx context.Context, unit string) bool {
	out, err := a.Runner.Run(ctx, "systemctl", "is-active", unit)
	return err == nil && out == "active"
}

func (a *Agent) handleSystemStatus(json.RawMessage) (any, error) {
	ctx := context.Background()
	facts, err := sysprobe.Probe(a.Paths.SitesRoot)
	if err != nil {
		return nil, err
	}
	cpuHeadroom := 100 - int(facts.Load1*100/float64(max(facts.CPUCount, 1)))
	if cpuHeadroom < 0 {
		cpuHeadroom = 0
	}
	if cpuHeadroom > 100 {
		cpuHeadroom = 100
	}
	return rpc.SystemStatusResult{
		CPUCount:        facts.CPUCount,
		Load1:           facts.Load1,
		MemTotalMB:      facts.MemTotalMB,
		MemAvailableMB:  facts.MemAvailableMB,
		DiskTotalMB:     facts.DiskTotalMB,
		DiskFreeMB:      facts.DiskFreeMB,
		NginxRunning:    a.serviceActive(ctx, "nginx"),
		MariaDBRunning:  a.serviceActive(ctx, "mariadb"),
		RedisRunning:    a.serviceActive(ctx, "redis-server"),
		AgentVersion:    version.Version,
		UptimeSeconds:   facts.UptimeSeconds,
		CPUHeadroomPct:  cpuHeadroom,
		MemHeadroomPct:  sysprobe.HeadroomPct(facts.MemAvailableMB, facts.MemTotalMB),
		DiskHeadroomPct: sysprobe.HeadroomPct(facts.DiskFreeMB, facts.DiskTotalMB),
	}, nil
}

// PurgeCache removes specific URLs from a site's page cache, or everything
// when no URLs are given.
func (a *Agent) PurgeCache(p rpc.PurgeParams) (rpc.PurgeResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.PurgeResult{}, err
	}
	cacheDir := filepath.Join(a.Paths.CacheRoot, velocity.SanitizeCacheDirName(p.Site.Domain))
	var removed int
	var err error
	if len(p.URLs) == 0 {
		removed, err = velocity.PurgeAll(cacheDir)
	} else {
		removed, err = velocity.PurgeURLs(cacheDir, p.URLs)
	}
	if err != nil {
		return rpc.PurgeResult{}, err
	}
	return rpc.PurgeResult{Removed: removed}, nil
}

// CheckDrift hashes every managed file and reports the ones that changed
// outside the panel.
func (a *Agent) CheckDrift(p rpc.DriftParams) (rpc.DriftResult, error) {
	var res rpc.DriftResult
	for path, expected := range p.Expected {
		actual, err := hashFile(path)
		if err != nil {
			return res, err
		}
		if actual != expected {
			res.Drifted = append(res.Drifted, rpc.DriftedFile{
				Path: path, ExpectedHash: expected, ActualHash: actual,
			})
		}
	}
	return res, nil
}
