package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/engine"
	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/phpfpm"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
	"github.com/slipstream-panel/slipstream/internal/sysprobe"
	"github.com/slipstream-panel/slipstream/internal/velocity"
)

var systemUserRe = regexp.MustCompile(`^slip-site-[0-9]+$`)
var phpVersionRe = regexp.MustCompile(`^8\.[0-9]$`)
var adminerTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func validateSite(site state.Site) error {
	if err := nginx.ValidateDomain(site.Domain); err != nil {
		return err
	}
	if !systemUserRe.MatchString(site.SystemUser) {
		return fmt.Errorf("invalid system user %q", site.SystemUser)
	}
	if site.PHPVersion != "" && !phpVersionRe.MatchString(site.PHPVersion) {
		return fmt.Errorf("unsupported php version %q", site.PHPVersion)
	}
	return nil
}

// input assembles the engine render input for a site.
func (a *Agent) input(site state.Site) engine.Input {
	docRoot := filepath.Join(site.RootPath, "current")
	if site.Config.PublicRoot != "" {
		docRoot = filepath.Join(docRoot, site.Config.PublicRoot)
	}
	certDir := filepath.Join(a.Paths.CertLiveDir, site.Domain)
	fullchain := filepath.Join(certDir, "fullchain.pem")
	in := engine.Input{
		Site:          site,
		Policy:        velocity.PolicyFor(site),
		DocRoot:       docRoot,
		CacheDir:      filepath.Join(a.Paths.CacheRoot, velocity.SanitizeCacheDirName(site.Domain)),
		LogDir:        filepath.Join(a.Paths.LogRoot, site.Domain),
		CertFullchain: fullchain,
		CertKey:       filepath.Join(certDir, "privkey.pem"),
		FallbackCert:  a.Paths.FallbackCert,
		FallbackKey:   a.Paths.FallbackKey,
		ACMEWebroot:   a.Paths.ACMEWebroot,
	}
	if site.Type != state.SiteStatic && site.Type != state.SiteProxy {
		in.PHPSocket = filepath.Join(a.Paths.PHPSocketDir, site.SystemUser+".sock")
	}
	if _, err := os.Stat(fullchain); err == nil {
		in.CertAvailable = true
	}
	return in
}

// renderSite writes all managed config for a site and reloads services.
// Engine-global files (log formats, compression defaults) are re-rendered
// too: the write is idempotent and guarantees vhosts never reference
// missing definitions.
func (a *Agent) renderSite(ctx context.Context, site state.Site) ([]rpc.ManagedFile, error) {
	in := a.input(site)
	files, err := a.Renderer.SiteFiles(in)
	if err != nil {
		return nil, err
	}
	for path, content := range a.Renderer.GlobalFiles() {
		files[path] = content
	}
	if in.Policy.Enabled {
		if err := os.MkdirAll(in.CacheDir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(in.LogDir, 0o755); err != nil {
		return nil, err
	}

	// Remember prior content so a failed nginx validation can roll back —
	// one bad render must never wedge reloads for every other site.
	type prior struct {
		content []byte
		existed bool
	}
	priors := map[string]prior{}
	rollback := func() {
		for path, p := range priors {
			if p.existed {
				os.WriteFile(path, p.content, 0o644)
			} else {
				os.Remove(path)
			}
		}
	}

	var managed []rpc.ManagedFile
	for path, content := range files {
		old, readErr := os.ReadFile(path)
		priors[path] = prior{content: old, existed: readErr == nil}
		mf, err := writeManaged(path, content, 0o644)
		if err != nil {
			rollback()
			return nil, err
		}
		managed = append(managed, mf)
	}

	if in.PHPSocket != "" {
		facts, _ := sysprobe.Probe(a.Paths.SitesRoot)
		siteMem := site.Config.Resources.MemoryHighMB
		if siteMem <= 0 && facts.MemTotalMB > 0 {
			siteMem = int(facts.MemTotalMB / 4)
		}
		poolPath, poolContent, err := phpfpm.RenderPool(site, siteMem)
		if err != nil {
			rollback()
			return nil, err
		}
		// Tests re-root pool files under a temp dir via PHPPoolRoot.
		if a.Paths.PHPPoolRoot != "/etc/php" {
			poolPath = filepath.Join(a.Paths.PHPPoolRoot, site.PHPVersion, "fpm/pool.d", site.SystemUser+".conf")
		}
		old, readErr := os.ReadFile(poolPath)
		priors[poolPath] = prior{content: old, existed: readErr == nil}
		mf, err := writeManaged(poolPath, poolContent, 0o644)
		if err != nil {
			rollback()
			return nil, err
		}
		managed = append(managed, mf)
		if err := a.reloadPHPFPM(ctx, site.PHPVersion); err != nil {
			rollback()
			return nil, err
		}
	}

	if err := a.reloadNginx(); err != nil {
		rollback()
		return nil, err
	}
	return managed, nil
}

func (a *Agent) reloadNginx() error {
	ctx := context.Background()
	if _, err := a.Runner.Run(ctx, "nginx", "-t"); err != nil {
		return fmt.Errorf("nginx config test failed: %w", err)
	}
	_, err := a.Runner.Run(ctx, "systemctl", "reload", "nginx")
	return err
}

func (a *Agent) reloadPHPFPM(ctx context.Context, phpVersion string) error {
	_, err := a.Runner.Run(ctx, "systemctl", "reload", "php"+phpVersion+"-fpm")
	return err
}

// CreateSite provisions a complete site: Unix user, directory skeleton,
// database, application bootstrap, rendered configuration, and reloads.
func (a *Agent) CreateSite(p rpc.CreateSiteParams) (rpc.CreateSiteResult, error) {
	ctx := context.Background()
	site := p.Site
	if err := validateSite(site); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	if site.RootPath == "" {
		site.RootPath = filepath.Join(a.Paths.SitesRoot, site.Domain)
	}
	if err := a.validateSiteRoot(site); err != nil {
		return rpc.CreateSiteResult{}, err
	}

	// Any failure before the site is fully provisioned tears down the
	// half-created user, database and directory tree — otherwise a retry
	// dies at useradd ("user exists") and the site can never be recreated.
	provisioned := false
	defer func() {
		if provisioned {
			return
		}
		a.Log.Warn("provisioning failed, rolling back", "site", site.Domain)
		if _, err := a.DeleteSite(rpc.SiteRef{Site: site}); err != nil {
			a.Log.Error("provisioning rollback incomplete", "site", site.Domain, "err", err)
		}
	}()

	// 1. Isolated Unix identity.
	if _, err := a.Runner.Run(ctx, "useradd", "--system", "--no-create-home",
		"--home-dir", site.RootPath, "--shell", "/usr/sbin/nologin", site.SystemUser); err != nil {
		return rpc.CreateSiteResult{}, fmt.Errorf("create user: %w", err)
	}

	// 2. Directory skeleton: immutable releases + shared persistent data.
	for _, d := range []string{
		"releases", "shared/uploads", "logs", "tmp/sessions",
	} {
		if err := os.MkdirAll(filepath.Join(site.RootPath, d), 0o750); err != nil {
			return rpc.CreateSiteResult{}, err
		}
	}
	if _, err := a.Runner.Run(ctx, "chown", "-R", site.SystemUser+":"+site.SystemUser, site.RootPath); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	// OpenSSH requires every ChrootDirectory component to be root-owned and
	// non-writable by the jailed user. Children remain site-owned/writable.
	if _, err := a.Runner.Run(ctx, "chown", "root:"+site.SystemUser, site.RootPath); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	// The site user and Nginx (a member of the site group) need traversal.
	if _, err := a.Runner.Run(ctx, "chmod", "750", site.RootPath); err != nil {
		return rpc.CreateSiteResult{}, err
	}
	if _, err := a.Runner.Run(ctx, "usermod", "-aG", site.SystemUser, "www-data"); err != nil {
		return rpc.CreateSiteResult{}, err
	}

	result := rpc.CreateSiteResult{SystemUser: site.SystemUser, RootPath: site.RootPath}

	// 3. Database.
	if site.Config.Database.Enabled && !site.Config.Database.External {
		dbp := rpc.DatabaseParams{
			Name:     site.Config.Database.Name,
			User:     site.Config.Database.User,
			Password: p.DBPassword,
			MaxConns: site.Config.Resources.DBConnections,
		}
		if _, err := a.CreateDatabase(dbp); err != nil {
			return result, err
		}
		result.DatabaseName, result.DatabaseUser = dbp.Name, dbp.User
	}

	// 4. First release directory and the current symlink. The release dir
	// must belong to the site user before the app bootstrap: wp-cli runs
	// as that user and writes here.
	releaseDir := filepath.Join(site.RootPath, "releases", "initial")
	if err := os.MkdirAll(releaseDir, 0o750); err != nil {
		return result, err
	}
	if _, err := a.Runner.Run(ctx, "chown", "-R", site.SystemUser+":"+site.SystemUser, releaseDir); err != nil {
		return result, err
	}
	if err := forceSymlink(releaseDir, filepath.Join(site.RootPath, "current")); err != nil {
		return result, err
	}

	// 5. Application bootstrap.
	switch site.Type {
	case state.SiteWordPress, state.SiteWooCommerce:
		if err := a.installWordPress(ctx, site, releaseDir, p); err != nil {
			return result, err
		}
		// Object cache on by default: WooCommerce/commerce benefit most, but
		// even a blog's cold renders and logged-in paths get faster. APCu is
		// the single-server backend (no daemon).
		if _, err := a.WPObjectCache(rpc.WPParams{Site: site, Enable: true}); err != nil {
			a.Log.Warn("object cache setup failed", "site", site.Domain, "err", err)
		}
		// Commerce sites: tame the cart-fragments AJAX that punches through
		// the page cache on every anonymous page.
		if site.Type == state.SiteWooCommerce || site.Profile == state.ProfileCommerce {
			run := func(args ...string) { a.wp(ctx, site, args...) }
			run("config", "set", "SLIPSTREAM_DISABLE_CART_FRAGMENTS", "true", "--raw")
		}
	case state.SiteStatic:
		index := filepath.Join(releaseDir, "index.html")
		if err := os.WriteFile(index, []byte(placeholderHTML(site.Domain)), 0o644); err != nil {
			return result, err
		}
	case state.SitePHP, state.SiteLaravel:
		// Same "launched" placeholder as static sites, but as PHP so a fresh
		// site actually serves something instead of a bare 403 until the
		// operator deploys real code. Laravel's docroot is releases/*/public.
		docRoot := releaseDir
		if site.Config.PublicRoot != "" {
			docRoot = filepath.Join(releaseDir, site.Config.PublicRoot)
			if err := os.MkdirAll(docRoot, 0o750); err != nil {
				return result, err
			}
		}
		index := filepath.Join(docRoot, "index.php")
		if err := os.WriteFile(index, []byte(placeholderPHP(site.Domain)), 0o644); err != nil {
			return result, err
		}
	}

	// Precompress static assets so nginx serves ready-made .gz.
	precompressTree(releaseDir)
	if _, err := a.Runner.Run(ctx, "chown", "-R", site.SystemUser+":"+site.SystemUser, releaseDir); err != nil {
		return result, err
	}

	// 6. Rendered configuration + service reloads.
	files, err := a.renderSite(ctx, site)
	if err != nil {
		return result, err
	}
	result.Files = files
	if err := a.verifyProvisionedSite(ctx, site); err != nil {
		return result, fmt.Errorf("verify provisioned site: %w", err)
	}
	provisioned = true
	return result, nil
}

func (a *Agent) verifyProvisionedSite(ctx context.Context, site state.Site) error {
	if _, err := a.Runner.Run(ctx, "id", "-u", site.SystemUser); err != nil {
		return fmt.Errorf("site identity unavailable: %w", err)
	}
	current, err := filepath.EvalSymlinks(filepath.Join(site.RootPath, "current"))
	if err != nil {
		return fmt.Errorf("current release unavailable: %w", err)
	}
	releases := filepath.Join(site.RootPath, "releases") + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(current)+string(os.PathSeparator), releases) {
		return fmt.Errorf("current release points outside the site")
	}
	if _, err := os.Stat(filepath.Join(a.Paths.NginxSites, site.Domain+".conf")); err != nil {
		return fmt.Errorf("nginx site configuration unavailable: %w", err)
	}
	if site.PHPVersion != "" {
		poolPath := filepath.Join(a.Paths.PHPPoolRoot, site.PHPVersion, "fpm/pool.d", site.SystemUser+".conf")
		if a.Paths.PHPPoolRoot == "/etc/php" {
			poolPath = filepath.Join(phpfpm.PoolDirFor(site.PHPVersion), site.SystemUser+".conf")
		}
		if _, err := os.Stat(poolPath); err != nil {
			return fmt.Errorf("PHP pool configuration unavailable: %w", err)
		}
	}
	if site.Config.Database.Enabled && !site.Config.Database.External {
		if err := a.mysqlExec(ctx, fmt.Sprintf("USE `%s`; SELECT 1;", site.Config.Database.Name)); err != nil {
			return fmt.Errorf("database unavailable: %w", err)
		}
	}
	return nil
}

func (a *Agent) installWordPress(ctx context.Context, site state.Site, dir string, p rpc.CreateSiteParams) error {
	run := func(args ...string) error {
		argv := append([]string{"-u", site.SystemUser, "--", "wp", "--path=" + dir}, args...)
		_, err := a.Runner.Run(ctx, "runuser", argv...)
		return err
	}
	// runPrompt feeds a secret to wp-cli via stdin (--prompt=<param>) so the
	// password never lands in argv / /proc/<pid>/cmdline.
	runPrompt := func(secret string, args ...string) error {
		argv := append([]string{"-u", site.SystemUser, "--", "wp", "--path=" + dir}, args...)
		_, err := a.Runner.RunStdin(ctx, secret+"\n", "runuser", argv...)
		return err
	}
	if err := run("core", "download", "--locale=en_US"); err != nil {
		return fmt.Errorf("wp core download: %w", err)
	}
	db := site.Config.Database
	if err := runPrompt(p.DBPassword, "config", "create",
		"--dbname="+db.Name, "--dbuser="+db.User, "--prompt=dbpass",
		"--dbhost="+fmt.Sprintf("%s:%d", db.Host, db.Port), "--skip-check"); err != nil {
		return fmt.Errorf("wp config create: %w", err)
	}
	if p.ConnectorToken != "" {
		if err := run("config", "set", "SLIPSTREAM_SITE_TOKEN", p.ConnectorToken); err != nil {
			return fmt.Errorf("wp config set connector token: %w", err)
		}
		if err := run("config", "set", "SLIPSTREAM_ENDPOINT", "http://127.0.0.1:9080"); err != nil {
			return fmt.Errorf("wp config set connector endpoint: %w", err)
		}
	}
	title := p.SiteTitle
	if title == "" {
		title = site.Domain
	}
	if err := runPrompt(p.AdminPassword, "core", "install",
		"--url=https://"+site.Domain, "--title="+title,
		"--admin_user="+p.AdminUser, "--prompt=admin_password",
		"--admin_email="+p.AdminEmail, "--skip-email"); err != nil {
		return fmt.Errorf("wp core install: %w", err)
	}
	// Pretty permalinks by default: better for SEO, and required for the
	// core sitemap (which cache-warming crawls) to resolve.
	run("rewrite", "structure", "/%postname%/", "--hard")
	// Configuration lives in shared/, not in the release: deployments carry
	// code, never credentials. wp-cli edits follow the symlink. (If wp-cli
	// produced no config file it already failed loudly above.)
	cfg := filepath.Join(dir, "wp-config.php")
	sharedCfg := filepath.Join(site.RootPath, "shared", "wp-config.php")
	if _, err := os.Stat(cfg); err == nil {
		if err := os.Rename(cfg, sharedCfg); err != nil {
			return fmt.Errorf("move wp-config to shared: %w", err)
		}
		if err := forceSymlink(sharedCfg, cfg); err != nil {
			return err
		}
	}

	// Move uploads into shared/ so releases stay immutable.
	uploads := filepath.Join(dir, "wp-content", "uploads")
	if err := os.MkdirAll(filepath.Dir(uploads), 0o750); err != nil {
		return err
	}
	os.RemoveAll(uploads)
	if err := forceSymlink(filepath.Join(site.RootPath, "shared", "uploads"), uploads); err != nil {
		return err
	}
	// Install the Slipstream connector for precise cache invalidation.
	muDir := filepath.Join(dir, "wp-content", "mu-plugins")
	if err := os.MkdirAll(muDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(muDir, "slipstream-connector.php"), []byte(ConnectorPHP), 0o644)
}

// DeleteSite tears down a site completely.
func (a *Agent) DeleteSite(p rpc.SiteRef) (map[string]any, error) {
	ctx := context.Background()
	site := p.Site
	if err := validateSite(site); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(site); err != nil {
		return nil, err
	}
	var cleanupErrs []error
	remove := func(path string) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	// Remove rendered config first so nginx stops routing.
	remove(filepath.Join(a.Paths.NginxSites, site.Domain+".conf"))
	remove(filepath.Join(a.Paths.NginxConfDir, fmt.Sprintf("slipstream-cache-%d.conf", site.ID)))
	if site.PHPVersion != "" {
		poolPath := filepath.Join(a.Paths.PHPPoolRoot, site.PHPVersion, "fpm/pool.d", site.SystemUser+".conf")
		if a.Paths.PHPPoolRoot == "/etc/php" {
			poolPath = filepath.Join(phpfpm.PoolDirFor(site.PHPVersion), site.SystemUser+".conf")
		}
		remove(poolPath)
	}
	if site.Config.Database.Enabled && !site.Config.Database.External {
		if _, err := a.DropDatabase(rpc.DatabaseParams{Name: site.Config.Database.Name, User: site.Config.Database.User}); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	if err := os.RemoveAll(filepath.Join(a.Paths.CacheRoot, velocity.SanitizeCacheDirName(site.Domain))); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove cache: %w", err))
	}
	if err := os.RemoveAll(site.RootPath); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove site files: %w", err))
	}
	// Remove any Let's Encrypt certificate + renewal config for this domain.
	// Otherwise certbot's timer keeps trying (and failing) to renew a cert for
	// a domain that no longer resolves here, and orphaned configs pile up.
	// Best-effort: most deletes have no certificate.
	a.Runner.Run(ctx, "certbot", "delete", "--cert-name", site.Domain, "--non-interactive")
	// Stop the site's PHP-FPM workers BEFORE removing its Unix identity. The
	// pool file was removed above, so reloading drops the pool — but a graceful
	// reload lets in-flight workers finish, and userdel(8) refuses to remove a
	// user that still owns a live process (exit 8). That race used to strand the
	// identity and fail the whole delete, leaving the site stuck in "error".
	if site.PHPVersion != "" {
		if err := a.reloadPHPFPM(ctx, site.PHPVersion); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	// userdel is not idempotent, so only invoke it while the identity exists.
	if _, err := a.Runner.Run(ctx, "id", "-u", site.SystemUser); err == nil {
		// Kill any straggler processes still owned by the identity, then retry
		// userdel briefly — a SIGKILL is not always reaped the instant pkill
		// returns, and a worker may exit a moment after the reload.
		a.Runner.Run(ctx, "pkill", "-9", "-u", site.SystemUser)
		var delErr error
		for attempt := 0; attempt < 5; attempt++ {
			if _, delErr = a.Runner.Run(ctx, "userdel", site.SystemUser); delErr == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
			a.Runner.Run(ctx, "pkill", "-9", "-u", site.SystemUser)
		}
		if delErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove user: %w", delErr))
		}
	}
	if err := a.reloadNginx(); err != nil {
		cleanupErrs = append(cleanupErrs, err)
	}
	if err := errors.Join(cleanupErrs...); err != nil {
		return nil, fmt.Errorf("site cleanup incomplete: %w", err)
	}
	return map[string]any{"deleted": site.Domain}, nil
}

func (a *Agent) validateSiteRoot(site state.Site) error {
	expected := filepath.Join(a.Paths.SitesRoot, site.Domain)
	if filepath.Clean(site.RootPath) != filepath.Clean(expected) {
		return fmt.Errorf("invalid site root %q: expected %q", site.RootPath, expected)
	}
	return nil
}

// ApplySiteConfig re-renders a site's configuration from desired state.
func (a *Agent) ApplySiteConfig(p rpc.SiteRef) (rpc.ApplyResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.ApplyResult{}, err
	}
	files, err := a.renderSite(context.Background(), p.Site)
	if err != nil {
		return rpc.ApplyResult{}, err
	}
	return rpc.ApplyResult{Files: files}, nil
}

func forceSymlink(target, link string) error {
	tmp := link + ".slipstream-tmp"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// placeholderPHP mirrors placeholderHTML but proves PHP itself is executing
// (host/version echoed via PHP, not templated in from Go) rather than just
// serving static markup with a .php extension.
func placeholderPHP(domain string) string {
	return `<?php $host = htmlspecialchars($_SERVER['HTTP_HOST'] ?? ` + phpStr(domain) + `, ENT_QUOTES); ?>
<!doctype html><html><head><meta charset="utf-8"><title><?php echo $host; ?></title></head>
<body style="font-family:system-ui;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center"><h1><?php echo $host; ?></h1><p>Launched with Slipstream. PHP <?php echo htmlspecialchars(phpversion(), ENT_QUOTES); ?> is running &mdash; deploy your content to replace this page.</p></div>
</body></html>
`
}

func placeholderHTML(domain string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + domain + `</title></head>
<body style="font-family:system-ui;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center"><h1>` + domain + `</h1><p>Launched with Slipstream. Deploy your content to replace this page.</p></div>
</body></html>
`
}
