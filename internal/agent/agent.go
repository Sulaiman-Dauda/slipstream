// Package agent implements Slipstream's privileged daemon. It accepts
// typed commands over the RPC socket and performs the actual system work:
// provisioning, releases, backups, certificates, cache purges and drift
// checks. It is deliberately stateless — desired state lives in the API's
// SQLite database and arrives with each command.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/slipstream-panel/slipstream/internal/engine"
	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/version"
)

// Paths configures where the agent puts things. Defaults match the
// installer layout; tests point them into temp directories.
type Paths struct {
	SitesRoot    string // /srv/sites
	CacheRoot    string // /var/cache/slipstream
	LogRoot      string // /var/log/slipstream
	ACMEWebroot  string // /var/lib/slipstream/acme
	FallbackCert string // /etc/slipstream/certs/fallback.pem
	FallbackKey  string // /etc/slipstream/certs/fallback.key
	CertLiveDir  string // /etc/letsencrypt/live
	NginxSites   string // /etc/nginx/sites-enabled
	NginxConfDir string // /etc/nginx/conf.d
	PHPPoolRoot  string // /etc/php (…/<ver>/fpm/pool.d)
	PHPSocketDir string // /run/slipstream/php
	WorkDir      string // /var/lib/slipstream/work (restore tests, staging dumps)
}

// DefaultPaths is the production layout created by the installer.
func DefaultPaths() Paths {
	return Paths{
		SitesRoot: "/srv/sites",
		CacheRoot: "/var/cache/slipstream",
		LogRoot:   "/var/log/slipstream",
		// Outside /var/lib/slipstream on purpose: nginx (www-data) must be
		// able to traverse to the challenge files, and the panel state dir
		// is deliberately closed to it.
		ACMEWebroot:  "/var/www/slipstream-acme",
		FallbackCert: "/etc/slipstream/certs/fallback.pem",
		FallbackKey:  "/etc/slipstream/certs/fallback.key",
		CertLiveDir:  "/etc/letsencrypt/live",
		NginxSites:   "/etc/nginx/sites-enabled",
		NginxConfDir: "/etc/nginx/conf.d",
		PHPPoolRoot:  "/etc/php",
		PHPSocketDir: "/run/slipstream/php",
		WorkDir:      "/var/lib/slipstream/work",
	}
}

// Agent executes privileged operations.
type Agent struct {
	Paths    Paths
	Runner   Runner
	Renderer engine.Renderer
	Log      *slog.Logger
	certMu   sync.Mutex // certbot permits only one process against its state
	repoMu   sync.Mutex // concurrent `restic init` against the same repo corrupts it

	http3Once sync.Once // nginx HTTP/3 support is probed once per process
	http3     bool
}

// New creates an agent with production defaults.
func New(logger *slog.Logger) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	paths := DefaultPaths()
	return &Agent{
		Paths:    paths,
		Runner:   ExecRunner{},
		Renderer: nginx.Renderer{SitesDir: paths.NginxSites, ConfDir: paths.NginxConfDir},
		Log:      logger,
	}
}

// RegisterAll wires every RPC method to its handler.
func (a *Agent) RegisterAll(s *rpc.Server) {
	s.Handle(rpc.MethodPing, func(json.RawMessage) (any, error) {
		return map[string]string{"version": version.Version}, nil
	})
	s.Handle(rpc.MethodSystemStatus, a.handleSystemStatus)
	s.Handle(rpc.MethodCreateSite, typed(a.CreateSite))
	s.Handle(rpc.MethodDeleteSite, typed(a.DeleteSite))
	s.Handle(rpc.MethodApplySiteConfig, typed(a.ApplySiteConfig))
	s.Handle(rpc.MethodIssueCertificate, typed(a.IssueCertificate))
	s.Handle(rpc.MethodCreateDatabase, typed(a.CreateDatabase))
	s.Handle(rpc.MethodDropDatabase, typed(a.DropDatabase))
	s.Handle(rpc.MethodDeployRelease, typed(a.DeployRelease))
	s.Handle(rpc.MethodPromoteRelease, typed(a.PromoteRelease))
	s.Handle(rpc.MethodRollbackRelease, typed(a.RollbackRelease))
	s.Handle(rpc.MethodCreateStaging, typed(a.CreateStaging))
	s.Handle(rpc.MethodSyncStagingDB, typed(a.SyncStagingDatabase))
	s.Handle(rpc.MethodRunBackup, typed(a.RunBackup))
	s.Handle(rpc.MethodRestoreSnapshot, typed(a.RestoreSnapshot))
	s.Handle(rpc.MethodVerifyBackup, typed(a.VerifyBackup))
	s.Handle(rpc.MethodPurgeCache, typed(a.PurgeCache))
	s.Handle(rpc.MethodReloadWebServer, func(json.RawMessage) (any, error) {
		return nil, a.reloadNginx()
	})
	s.Handle(rpc.MethodCheckDrift, typed(a.CheckDrift))

	// v1.1 / v1.2
	s.Handle(rpc.MethodRestartService, typed(a.RestartService))
	s.Handle(rpc.MethodServiceStatus, func(json.RawMessage) (any, error) { return a.ServiceStatus() })
	s.Handle(rpc.MethodTailLog, typed(a.TailLog))
	s.Handle(rpc.MethodWriteCrontab, typed(a.WriteCrontab))
	s.Handle(rpc.MethodRunCron, typed(a.RunCron))
	s.Handle(rpc.MethodFirewallStatus, func(json.RawMessage) (any, error) { return a.FirewallStatus() })
	s.Handle(rpc.MethodFirewallRule, typed(a.FirewallRule))
	s.Handle(rpc.MethodDBQuery, typed(a.DBQuery))
	s.Handle(rpc.MethodDBExport, typed(a.DBExport))
	s.Handle(rpc.MethodDBImport, typed(a.DBImport))
	s.Handle(rpc.MethodLaunchAdminer, typed(a.LaunchAdminer))
	s.Handle(rpc.MethodListFiles, typed(a.ListFiles))
	s.Handle(rpc.MethodReadFile, typed(a.ReadFile))
	s.Handle(rpc.MethodWriteFile, typed(a.WriteFile))
	s.Handle(rpc.MethodTransferFile, typed(a.TransferFile))
	s.Handle(rpc.MethodManageFile, typed(a.ManageFile))
	s.Handle(rpc.MethodSetSFTP, typed(a.SetSFTP))
	s.Handle(rpc.MethodSSHKeys, typed(a.SSHKeys))
	s.Handle(rpc.MethodWPMagicLogin, typed(a.WPMagicLogin))
	s.Handle(rpc.MethodWPPlugins, typed(a.WPPlugins))
	s.Handle(rpc.MethodWPUpdate, typed(a.WPUpdate))
	s.Handle(rpc.MethodWPObjectCache, typed(a.WPObjectCache))
	s.Handle(rpc.MethodPanelCert, typed(a.PanelCertificate))
	s.Handle(rpc.MethodSelfUpdate, typed(a.SelfUpdate))
	s.Handle(rpc.MethodCacheStats, typed(a.CacheStats))
	s.Handle(rpc.MethodWarmCache, typed(a.WarmCache))
	s.Handle(rpc.MethodTestBackup, typed(a.TestBackup))
	s.Handle(rpc.MethodImportMigration, typed(a.ImportMigration))
}

// typed adapts a strongly-typed handler to the raw RPC signature.
func typed[P any, R any](fn func(P) (R, error)) rpc.HandlerFunc {
	return func(raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("bad params: %w", err)
			}
		}
		return fn(p)
	}
}

// writeManaged atomically writes a panel-owned file and returns its hash.
func writeManaged(path, content string, mode os.FileMode) (rpc.ManagedFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return rpc.ManagedFile{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".slipstream-*")
	if err != nil {
		return rpc.ManagedFile{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return rpc.ManagedFile{}, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return rpc.ManagedFile{}, err
	}
	if err := tmp.Close(); err != nil {
		return rpc.ManagedFile{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return rpc.ManagedFile{}, err
	}
	sum := sha256.Sum256([]byte(content))
	return rpc.ManagedFile{Path: path, SHA256: hex.EncodeToString(sum[:])}, nil
}

// hashFile returns the sha256 of a file's content, or "" if it is missing.
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
