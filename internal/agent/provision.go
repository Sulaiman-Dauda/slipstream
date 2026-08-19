package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/engine"
	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/phpfpm"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
	"github.com/slipstream-panel/slipstream/internal/sysprobe"
	"github.com/slipstream-panel/slipstream/internal/velocity"
	"github.com/slipstream-panel/slipstream/internal/version"
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
	in.HTTP3 = a.nginxHasHTTP3()
	return in
}

// installedPHPVersions lists the PHP versions actually installed on this host,
// newest first, e.g. ["8.5", "8.4"].
//
// Detection is by FPM binary (/usr/sbin/php-fpm<version>), NOT by the presence
// of /etc/php/<version>/. A pool directory proves nothing: rendering a pool
// creates its parents, so a single failed provision for an uninstalled version
// leaves an empty /etc/php/<version>/fpm/pool.d/ behind forever, and treating
// that as "installed" reintroduces exactly the failure this guards against.
func (a *Agent) installedPHPVersions() []string {
	entries, err := os.ReadDir(a.Paths.PHPPoolRoot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !phpVersionRe.MatchString(e.Name()) {
			continue
		}
		if _, err := os.Stat(a.phpFPMBinary(e.Name())); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// phpFPMBinary is the path Debian/Ubuntu installs the FPM binary at. Overridable
// so tests can point it at a temp tree.
func (a *Agent) phpFPMBinary(version string) string {
	dir := a.Paths.PHPBinDir
	if dir == "" {
		dir = "/usr/sbin"
	}
	return filepath.Join(dir, "php-fpm"+version)
}

// validatePHPVersionInstalled rejects a version this host cannot serve, naming
// the ones it can. An empty version means "use the panel default" and is fine.
func (a *Agent) validatePHPVersionInstalled(version string) error {
	if version == "" {
		return nil
	}
	installed := a.installedPHPVersions()
	if len(installed) == 0 {
		// Can't enumerate (unusual layout, or a test tree) — don't block.
		return nil
	}
	for _, v := range installed {
		if v == version {
			return nil
		}
	}
	return fmt.Errorf("PHP %s is not installed on this server (available: %s)",
		version, strings.Join(installed, ", "))
}

// nginxHasHTTP3 reports whether the installed nginx was built with
// ngx_http_v3_module. Ubuntu 24.04 ships nginx 1.24 (no HTTP/3), 26.04 and
// newer ship a build that has it — so sites gain QUIC when the OS provides it,
// without Slipstream vendoring its own nginx. The probe result is cached: it
// cannot change without replacing the binary, which restarts the agent anyway.
func (a *Agent) nginxHasHTTP3() bool {
	a.http3Once.Do(func() {
		// `nginx -V` prints its configure line to STDERR and exits 0, so this
		// must read combined output — reading stdout alone returns "" and
		// silently reports "no HTTP/3" even on a build that has it.
		out, err := a.Runner.RunCombined(context.Background(), "nginx", "-V")
		if err != nil {
			a.http3 = false
			return
		}
		a.http3 = strings.Contains(out, "http_v3_module")
	})
	return a.http3
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
			sites := countProvisionedSites(a.Paths.SitesRoot)
			var floored bool
			siteMem, floored = autoSiteBudgetMB(facts.MemTotalMB, sites)
			if floored {
				a.Log.Warn("site memory budget floored: this server is over-subscribed",
					"sites", sites, "mem_total_mb", facts.MemTotalMB, "budget_mb", siteMem)
			}
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
		// Managed [global] FPM fragment (master self-heal, FD limits). Written
		// alongside the pool so it exists before the pool is loaded, and
		// re-asserted on every render so drift is corrected.
		gPath, gContent := phpfpm.RenderGlobal(site.PHPVersion)
		if a.Paths.PHPPoolRoot != "/etc/php" {
			gPath = filepath.Join(a.Paths.PHPPoolRoot, site.PHPVersion, "fpm/pool.d", phpfpm.GlobalConfName)
		}
		gOld, gErr := os.ReadFile(gPath)
		priors[gPath] = prior{content: gOld, existed: gErr == nil}
		gmf, err := writeManaged(gPath, gContent, 0o644)
		if err != nil {
			rollback()
			return nil, err
		}
		managed = append(managed, gmf)
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
	// Refuse a PHP version this machine does not have BEFORE creating a Unix
	// user, a database and a directory tree. Asking for 8.4 on a host that ships
	// 8.5 otherwise fails much later with "Unit php8.4-fpm.service not found",
	// and the rollback then trips over the same missing unit and reports
	// "rollback incomplete" — a confusing failure for what is a simple typo.
	if err := a.validatePHPVersionInstalled(site.PHPVersion); err != nil {
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
		// Commerce sites: actually install WooCommerce, then tame the
		// cart-fragments AJAX that punches through the page cache on every
		// anonymous page. Without the install step the "woocommerce" site type
		// is just WordPress with a dangling SLIPSTREAM_DISABLE_CART_FRAGMENTS
		// constant — /shop, /cart and /product/* all 404.
		if site.Type == state.SiteWooCommerce || site.Profile == state.ProfileCommerce {
			if err := a.installWooCommerce(ctx, site, releaseDir); err != nil {
				return result, fmt.Errorf("woocommerce install: %w", err)
			}
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
	return installConnector(dir)
}

// autoSiteBudgetMB is the memory a site may size its PHP workers against when
// the operator has not set a limit.
//
// A quarter of the machine is the budget for one site: the rest belongs to
// MariaDB, nginx, the panel and the OS. That quarter is then shared, because
// the budget is a claim on one pool of memory and every site was previously
// handed the whole quarter regardless of how many existed. Four sites each
// sizing themselves for a quarter of RAM is how a box ends up in swap with
// every individual site looking correctly tuned.
//
// One site therefore gets exactly what it always did; only the multi-site
// case changes. The floor is the smallest budget that still yields the two
// workers SizeWorkers insists on, because a site with fewer cannot serve.
// It returns floored=true when the share fell below that minimum, which means
// the machine is genuinely over-subscribed: the sites cannot all be given a
// working budget out of the pool. That is reported rather than hidden, because
// the alternative is a box that swaps while every site's config looks sane.
func autoSiteBudgetMB(memTotalMB int64, sites int) (budget int, floored bool) {
	if sites < 1 {
		sites = 1
	}
	const floorMB = 160
	share := memTotalMB / 4 / int64(sites)
	if share < floorMB {
		return floorMB, true
	}
	return int(share), false
}

// countProvisionedSites counts site trees under the sites root. A directory
// counts once it has a releases/ directory, which is what provisioning
// creates, so a half-built site does not inflate the divisor.
func countProvisionedSites(sitesRoot string) int {
	entries, err := os.ReadDir(sitesRoot)
	if err != nil {
		return 1
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fi, err := os.Stat(filepath.Join(sitesRoot, e.Name(), "releases")); err == nil && fi.IsDir() {
			n++
		}
	}
	if n < 1 {
		n = 1
	}
	return n
}

// installConnector writes the cache-invalidation mu-plugin into a WordPress
// tree. It is called on the staging directory rather than a live release, so
// the recursive chown in DeployRelease gives it the site's ownership.
//
// Both site creation and migration import need this. A migrated site that
// lacks it looks healthy and serves cached pages correctly, but nothing ever
// purges them: an edit stays invisible until fastcgi_cache_valid expires,
// which reads to the site owner as "my change did not save".
func installConnector(dir string) error {
	muDir := filepath.Join(dir, "wp-content", "mu-plugins")
	if err := os.MkdirAll(muDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(muDir, "slipstream-connector.php"), []byte(ConnectorPHP), 0o644)
}

// installWooCommerce installs and activates WooCommerce on an already-installed
// WordPress, creates its pages (shop/cart/checkout/my-account) and flushes the
// rewrite rules so /shop and /product/* resolve instead of 404ing. It finishes
// by clearing the APCu object cache: the plugin activation and page creation
// run under wp-cli, whose APCu segment is separate from PHP-FPM's, so without
// this the live site keeps serving the pre-WooCommerce rewrite state (real
// pages 404) until FPM is restarted for some other reason.
func (a *Agent) installWooCommerce(ctx context.Context, site state.Site, dir string) error {
	run := func(args ...string) error {
		argv := append([]string{"-u", site.SystemUser, "--", "wp", "--path=" + dir}, args...)
		_, err := a.Runner.Run(ctx, "runuser", argv...)
		return err
	}
	if err := run("plugin", "install", "woocommerce", "--activate"); err != nil {
		return fmt.Errorf("wp plugin install woocommerce: %w", err)
	}
	// Activation registers the product post type and Woo's rewrite rules, and
	// (via wc_create_pages on activation) creates the shop/cart/checkout/
	// my-account pages. Flush so those rules are persisted to the DB option.
	if err := run("rewrite", "flush"); err != nil {
		return fmt.Errorf("wp rewrite flush: %w", err)
	}
	// A reload is enough here and a per-site flush would be wrong: this runs
	// during initial provisioning, BEFORE renderSite has written the vhost, so
	// a loopback request for this domain lands on the default server (the panel
	// itself) rather than the site. There is also nothing to flush — a brand-new
	// site has served no requests, so its FPM workers hold no cached state. The
	// per-site flush is for later mutations (updates, restores, migrations),
	// where the vhost exists and APCu genuinely holds stale rows.
	if err := a.reloadPHPFPM(ctx, site.PHPVersion); err != nil {
		a.Log.Warn("php-fpm reload after woocommerce install failed", "site", site.Domain, "err", err)
	}
	return nil
}

// restartPHPFPM fully restarts the FPM service, dropping APCu shared memory
// (which a reload preserves). This is the blunt instrument: the service is
// shared by every site on this PHP version, so it briefly interrupts other
// tenants. Prefer refreshSiteState, which achieves the same thing for one site.
func (a *Agent) restartPHPFPM(ctx context.Context, phpVersion string) error {
	_, err := a.Runner.Run(ctx, "systemctl", "restart", "php"+phpVersion+"-fpm")
	return err
}

// refreshSiteState makes an out-of-band change to a site's database or files
// visible to the live site, touching only that site.
//
// Two caches hide such a change. OPcache holds compiled bytecode for the old
// files; an FPM reload clears it. APCu holds the site's WordPress object cache
// — options, rewrite rules — and a reload does NOT clear it, because SIGUSR2
// leaves the master (and its shared memory) alive. Restarting the service does
// clear it but interrupts every other tenant.
//
// So: ask the site itself to flush, by calling the connector through its own
// FPM pool over the loopback interface. That clears exactly this site's object
// cache. Then reload FPM for OPcache. Only if the flush cannot be delivered do
// we fall back to the service-wide restart, so correctness never depends on the
// connector being reachable.
func (a *Agent) refreshSiteState(ctx context.Context, site state.Site) error {
	if site.PHPVersion == "" {
		return nil
	}
	if site.Type == state.SiteWordPress || site.Type == state.SiteWooCommerce {
		if err := a.flushObjectCache(ctx, site); err == nil {
			// Object cache cleared for this site; reload handles OPcache.
			return a.reloadPHPFPM(ctx, site.PHPVersion)
		} else {
			a.Log.Warn("per-site object-cache flush failed; falling back to an FPM restart",
				"site", site.Domain, "err", err)
		}
	}
	return a.restartPHPFPM(ctx, site.PHPVersion)
}

// flushObjectCache asks the site's connector to run wp_cache_flush() inside one
// of its own FPM workers. The request goes to the loopback address with the
// site's Host header so nginx routes it to the right vhost and pool; the query
// string forces a page-cache bypass so PHP actually executes; the site token
// authenticates it and is sent as a header so it stays out of access logs.
func (a *Agent) flushObjectCache(ctx context.Context, site state.Site) error {
	token, err := a.wp(ctx, site, "config", "get", "SLIPSTREAM_SITE_TOKEN")
	if err != nil {
		return fmt.Errorf("read site token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("site has no connector token")
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Always connect to this machine, whatever the domain resolves to
			// publicly — the site may not be in DNS yet, or may point elsewhere.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, "127.0.0.1:443")
			},
			// The vhost may still be on the self-signed bootstrap certificate.
			// This is a loopback call to our own machine, so certificate
			// identity adds nothing: the site token is what authenticates it.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+site.Domain+"/?slipstream_flush=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Slipstream-Flush", token)
	req.Header.Set("User-Agent", "slipstream-agent/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("connector flush returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), `"flushed":true`) {
		return fmt.Errorf("connector did not flush: %s", strings.TrimSpace(string(body)))
	}
	return nil
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
	// Access/error logs outlive the site otherwise: every created-and-deleted
	// site left a directory in /var/log/slipstream forever, and logrotate has
	// nothing to prune once the vhost that wrote them is gone.
	if err := os.RemoveAll(filepath.Join(a.Paths.LogRoot, site.Domain)); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("remove logs: %w", err))
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
