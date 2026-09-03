package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// mockRunner records every command. cp is executed for real so file
// operations behave; everything else is a no-op with canned output.
type mockRunner struct {
	calls   [][]string
	stdins  []string
	fail    map[string]error
	outputs map[string]string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	m.calls = append(m.calls, append([]string{name}, args...))
	if err := m.fail[name]; err != nil {
		return "", err
	}
	if name == "cp" {
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		return string(out), err
	}
	if out, ok := m.outputs[name]; ok {
		return out, nil
	}
	return "", nil
}

func (m *mockRunner) RunStdin(ctx context.Context, stdin, name string, args ...string) (string, error) {
	m.stdins = append(m.stdins, stdin)
	return m.Run(ctx, name, args...)
}

// RunCombined serves the same canned outputs as Run; tests that care about
// stderr-only tools (nginx -V) seed m.outputs under a "<name> -V" key.
func (m *mockRunner) RunCombined(ctx context.Context, name string, args ...string) (string, error) {
	if out, ok := m.outputs[name+" "+strings.Join(args, " ")]; ok {
		m.calls = append(m.calls, append([]string{name}, args...))
		return out, nil
	}
	return m.Run(ctx, name, args...)
}

func (m *mockRunner) called(name string, wantArgs ...string) bool {
	for _, c := range m.calls {
		if c[0] != name {
			continue
		}
		joined := strings.Join(c, " ")
		ok := true
		for _, w := range wantArgs {
			if !strings.Contains(joined, w) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func testAgent(t *testing.T) (*Agent, *mockRunner) {
	t.Helper()
	root := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := &mockRunner{fail: map[string]error{}, outputs: map[string]string{"systemctl": "active"}}
	paths := Paths{
		SitesRoot:    mk("srv"),
		CacheRoot:    mk("cache"),
		LogRoot:      mk("logs"),
		ACMEWebroot:  mk("acme"),
		FallbackCert: filepath.Join(root, "certs", "fallback.pem"),
		FallbackKey:  filepath.Join(root, "certs", "fallback.key"),
		CertLiveDir:  mk("letsencrypt"),
		NginxSites:   mk("nginx", "sites-enabled"),
		NginxConfDir: mk("nginx", "conf.d"),
		PHPPoolRoot:  mk("php"),
		PHPSocketDir: mk("phpsock"),
		WorkDir:      mk("work"),
	}
	a := &Agent{
		Paths:    paths,
		Runner:   run,
		Renderer: nginx.Renderer{SitesDir: paths.NginxSites, ConfDir: paths.NginxConfDir},
		Log:      slog.Default(),
	}
	return a, run
}

func staticSite(a *Agent) state.Site {
	return state.Site{
		ID: 3, Domain: "docs.example.com", Type: state.SiteStatic,
		Profile: state.ProfileBalanced, Engine: state.EngineNginx,
		SystemUser: "slip-site-3",
		RootPath:   filepath.Join(a.Paths.SitesRoot, "docs.example.com"),
		Status:     state.SiteProvisioning,
	}
}

func TestCreateStaticSite(t *testing.T) {
	a, run := testAgent(t)
	site := staticSite(a)

	res, err := a.CreateSite(rpc.CreateSiteParams{Site: site})
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	if !run.called("useradd", "--system", "slip-site-3") {
		t.Error("expected useradd for isolated site user")
	}
	if !run.called("nginx", "-t") {
		t.Error("expected nginx config test before reload")
	}
	if !run.called("systemctl", "reload", "nginx") {
		t.Error("expected nginx reload")
	}

	// Directory skeleton + placeholder content.
	for _, p := range []string{
		"releases/initial/index.html", "shared/uploads", "logs", "tmp/sessions",
	} {
		if _, err := os.Stat(filepath.Join(site.RootPath, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	// current → releases/initial
	link, err := os.Readlink(filepath.Join(site.RootPath, "current"))
	if err != nil || filepath.Base(link) != "initial" {
		t.Errorf("current symlink: %s %v", link, err)
	}

	// Vhost rendered into the test nginx dir, hash reported for drift.
	vhostPath := filepath.Join(a.Paths.NginxSites, "docs.example.com.conf")
	b, err := os.ReadFile(vhostPath)
	if err != nil {
		t.Fatalf("vhost not written: %v", err)
	}
	if !strings.Contains(string(b), "server_name docs.example.com;") {
		t.Error("vhost content wrong")
	}
	found := false
	for _, f := range res.Files {
		if f.Path == vhostPath && f.SHA256 != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("managed file hash not reported: %+v", res.Files)
	}
}

// A freshly provisioned php/laravel site must serve something instead of a
// bare 403 until the operator deploys real code — the same "launched"
// placeholder static sites already got, just as PHP so it proves execution.
func TestCreatePHPAndLaravelSitesGetPlaceholder(t *testing.T) {
	a, _ := testAgent(t)

	php := staticSite(a)
	php.Type, php.Domain, php.SystemUser = state.SitePHP, "app.example.com", "slip-site-4"
	php.RootPath = filepath.Join(a.Paths.SitesRoot, php.Domain)
	php.PHPVersion = "8.4"
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: php}); err != nil {
		t.Fatalf("CreateSite(php): %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(php.RootPath, "releases/initial/index.php")); err != nil || !strings.Contains(string(b), "<?php") {
		t.Errorf("php site missing placeholder index.php: %v", err)
	}

	laravel := staticSite(a)
	laravel.Type, laravel.Domain, laravel.SystemUser = state.SiteLaravel, "shop.example.com", "slip-site-5"
	laravel.RootPath = filepath.Join(a.Paths.SitesRoot, laravel.Domain)
	laravel.PHPVersion = "8.4"
	laravel.Config.PublicRoot = "public"
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: laravel}); err != nil {
		t.Fatalf("CreateSite(laravel): %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(laravel.RootPath, "releases/initial/public/index.php")); err != nil || !strings.Contains(string(b), "<?php") {
		t.Errorf("laravel site missing placeholder public/index.php: %v", err)
	}
}

func TestCreateSiteRejectsBadInput(t *testing.T) {
	a, run := testAgent(t)
	bad := []state.Site{
		{Domain: "evil.com; }", SystemUser: "slip-site-1"},
		{Domain: "ok.example.com", SystemUser: "root"},
		{Domain: "ok.example.com", SystemUser: "slip-site-1", PHPVersion: "7.4; rm -rf /"},
		{Domain: "ok.example.com", SystemUser: "slip-site-1", RootPath: "/etc"},
	}
	for _, s := range bad {
		if _, err := a.CreateSite(rpc.CreateSiteParams{Site: s}); err == nil {
			t.Errorf("expected rejection of %+v", s)
		}
	}
	if len(run.calls) != 0 {
		t.Errorf("no commands may run for rejected input, got %v", run.calls)
	}
}

func TestDeleteSiteIsIdempotent(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DeleteSite(rpc.SiteRef{Site: site}); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := a.DeleteSite(rpc.SiteRef{Site: site}); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestDeployPromoteRollback(t *testing.T) {
	a, run := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}

	// Prepare a source tree.
	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "index.html"), []byte("v2"), 0o644)

	dep, err := a.DeployRelease(rpc.DeployParams{Site: site, SourceDir: src, ReleaseID: "20260711-120000"})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if dep.Checksum == "" {
		t.Error("expected release checksum")
	}
	// Deploying the same release twice must fail (immutability).
	if _, err := a.DeployRelease(rpc.DeployParams{Site: site, SourceDir: src, ReleaseID: "20260711-120000"}); err == nil {
		t.Error("expected duplicate release rejection")
	}

	if _, err := a.PromoteRelease(rpc.ReleaseParams{Site: site, ReleaseID: "20260711-120000"}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	link, _ := os.Readlink(filepath.Join(site.RootPath, "current"))
	if filepath.Base(link) != "20260711-120000" {
		t.Fatalf("current = %s, want new release", link)
	}

	// Rollback with no explicit target goes to the previous release.
	if _, err := a.RollbackRelease(rpc.ReleaseParams{Site: site}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	link, _ = os.Readlink(filepath.Join(site.RootPath, "current"))
	if filepath.Base(link) != "initial" {
		t.Fatalf("rollback landed on %s, want initial", link)
	}
	_ = run
}

func TestCreateStagingClonesAndRollsBackOnFailure(t *testing.T) {
	a, run := testAgent(t)
	prod := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: prod}); err != nil {
		t.Fatal(err)
	}
	stg := prod
	stg.ID = 4
	stg.Domain = "staging.docs.example.com"
	stg.SystemUser = "slip-site-4"
	stg.RootPath = filepath.Join(a.Paths.SitesRoot, stg.Domain)
	stg.StagingOf = prod.ID
	if _, err := a.CreateStaging(rpc.StagingParams{Production: prod, Staging: stg}); err != nil {
		t.Fatalf("create staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stg.RootPath, "releases", "initial", "index.html")); err != nil {
		t.Fatalf("staging content missing: %v", err)
	}
	if !run.called("chown", "root:slip-site-4", stg.RootPath) {
		t.Fatal("staging chroot ownership was not restored")
	}

	broken := stg
	broken.ID = 5
	broken.Domain = "broken.docs.example.com"
	broken.SystemUser = "slip-site-5"
	broken.RootPath = filepath.Join(a.Paths.SitesRoot, broken.Domain)
	run.fail["cp"] = errors.New("copy failed")
	if _, err := a.CreateStaging(rpc.StagingParams{Production: prod, Staging: broken}); err == nil {
		t.Fatal("expected staging clone failure")
	}
	if _, err := os.Stat(broken.RootPath); !os.IsNotExist(err) {
		t.Fatalf("failed staging root was not rolled back: %v", err)
	}
}

func TestProtectedStagingTables(t *testing.T) {
	wp := state.Site{Type: state.SiteWordPress}
	commerce := state.Site{Type: state.SiteWooCommerce}
	for _, table := range []string{"wp_users", "custom_usermeta"} {
		if !protectedLiveTable(wp, table) {
			t.Fatalf("WordPress identity table %s was not protected", table)
		}
	}
	for _, table := range []string{"wp_posts", "wp_postmeta", "wp_wc_orders", "wp_actionscheduler_actions", "wp_woocommerce_sessions"} {
		if !protectedLiveTable(commerce, table) {
			t.Fatalf("WooCommerce live table %s was not protected", table)
		}
	}
	if protectedLiveTable(commerce, "wp_terms") || protectedLiveTable(wp, "wp_options") {
		t.Fatal("safe selectable tables were incorrectly protected")
	}
	a, run := testAgent(t)
	prod := staticSite(a)
	prod.Type = state.SiteWooCommerce
	prod.PHPVersion = "8.4"
	prod.Config.Database = state.DatabaseConfig{Enabled: true, Name: "site_3", User: "site_3"}
	stg := prod
	stg.ID, stg.Domain, stg.SystemUser, stg.StagingOf = 4, "staging.docs.example.com", "slip-site-4", prod.ID
	stg.RootPath = filepath.Join(a.Paths.SitesRoot, stg.Domain)
	stg.Config.Database = state.DatabaseConfig{Enabled: true, Name: "site_4", User: "site_4"}
	if _, err := a.SyncStagingDatabase(rpc.SyncStagingDBParams{Production: prod, Staging: stg, Tables: []string{"wp_posts"}}); err == nil {
		t.Fatal("agent accepted protected WooCommerce table")
	}
	if run.called("mariadb") {
		t.Fatal("database command ran before protected-table rejection")
	}
}

func TestPurgeCacheAndDrift(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	site.Type = state.SiteWordPress
	site.PHPVersion = "8.4"
	site.Config.CacheEnabled = true

	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}

	// Seed a cache entry and purge it by URL.
	cacheDir := filepath.Join(a.Paths.CacheRoot, "docs.example.com")
	seedCacheEntry(t, cacheDir, "https://docs.example.com/post/")
	res, err := a.PurgeCache(rpc.PurgeParams{Site: site, URLs: []string{"https://docs.example.com/post/"}})
	if err != nil || res.Removed != 1 {
		t.Fatalf("purge: removed=%d err=%v", res.Removed, err)
	}

	// Drift: tamper with the rendered vhost.
	vhost := filepath.Join(a.Paths.NginxSites, "docs.example.com.conf")
	orig, _ := hashFile(vhost)
	os.WriteFile(vhost, []byte("# manual edit\n"), 0o644)
	drift, err := a.CheckDrift(rpc.DriftParams{Expected: map[string]string{vhost: orig}})
	if err != nil {
		t.Fatal(err)
	}
	if len(drift.Drifted) != 1 || drift.Drifted[0].Path != vhost {
		t.Fatalf("drift = %+v, want the tampered vhost", drift.Drifted)
	}
}

func TestDatabaseIdentifierValidation(t *testing.T) {
	a, run := testAgent(t)
	bad := []rpc.DatabaseParams{
		{Name: "x; DROP TABLE users", User: "ok_user", Password: strings.Repeat("p", 20)},
		{Name: "ok_db", User: "u'ser", Password: strings.Repeat("p", 20)},
		{Name: "ok_db", User: "ok_user", Password: "short"},
		{Name: "ok_db", User: "ok_user", Password: strings.Repeat("p", 20) + "'"},
	}
	for _, p := range bad {
		if _, err := a.CreateDatabase(p); err == nil {
			t.Errorf("expected rejection of %+v", p)
		}
	}
	if len(run.calls) != 0 {
		t.Errorf("no SQL may run for rejected identifiers, got %v", run.calls)
	}

	if _, err := a.CreateDatabase(rpc.DatabaseParams{Name: "site_db", User: "site_user", Password: strings.Repeat("s", 24)}); err != nil {
		t.Fatalf("valid database creation failed: %v", err)
	}
	// The SQL (with password) is fed on stdin, never argv — verify the
	// statement went through stdin and the password is NOT in any argv.
	foundSQL := false
	for _, s := range run.stdins {
		if strings.Contains(s, "CREATE DATABASE IF NOT EXISTS `site_db`") && strings.Contains(s, "IDENTIFIED BY 'ssssssssssssssssssssssss'") {
			foundSQL = true
		}
	}
	if !foundSQL {
		t.Error("expected CREATE DATABASE statement on stdin")
	}
	for _, c := range run.calls {
		if strings.Contains(strings.Join(c, " "), "ssssssssssssssssssssssss") {
			t.Error("password leaked into argv")
		}
	}
}

func TestRestoreRejectsUnknownModeBeforeRepositoryAccess(t *testing.T) {
	a, _ := testAgent(t)
	_, err := a.RestoreSnapshot(rpc.RestoreParams{
		Site:       staticSite(a),
		SnapshotID: "abcdef1234567890",
		Mode:       "everything",
	})
	if err == nil || !strings.Contains(err.Error(), "restore mode") {
		t.Fatalf("unknown restore mode error = %v", err)
	}
}

func TestDatabaseImportRejectsNonSQLPath(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	site.Config.Database = state.DatabaseConfig{Enabled: true, Name: "site_db", User: "site_user"}
	_, err := a.DBImport(rpc.DBImportParams{Site: site, Database: "site_db", RelPath: "shared/import.txt"})
	if err == nil || !strings.Contains(err.Error(), ".sql") {
		t.Fatalf("non-SQL import error = %v", err)
	}
}

func TestRunCronRejectsInvalidIdentityAndMultilineCommand(t *testing.T) {
	a, _ := testAgent(t)
	if _, err := a.RunCron(rpc.RunCronParams{SystemUser: "root;id", Command: "true"}); err == nil {
		t.Fatal("accepted invalid cron identity")
	}
	if _, err := a.RunCron(rpc.RunCronParams{SystemUser: "site-user", Command: "true\nfalse"}); err == nil {
		t.Fatal("accepted multiline cron command")
	}
}

// writeArchive builds a .tar.gz from the given headers (payload "x" for any
// regular file with Size > 0) and returns its path.
func writeArchive(t *testing.T, headers ...tar.Header) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "site.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i := range headers {
		if err := tw.WriteHeader(&headers[i]); err != nil {
			t.Fatal(err)
		}
		if headers[i].Typeflag == tar.TypeReg && headers[i].Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
	return archive
}

// A path-traversal name must still abort the whole extraction.
func TestMigrationArchiveRejectsTraversal(t *testing.T) {
	archive := writeArchive(t, tar.Header{Name: "../../etc/shadow", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	if _, _, _, err := extractMigrationArchive(archive, t.TempDir()); err == nil {
		t.Fatal("traversal archive was accepted")
	}
}

// An escaping symlink (and a write attempted through it) must never place a
// file outside the extraction root — but must no longer fail the migration:
// the real files extract and the unsafe link is skipped.
func TestMigrationArchiveSkipsEscapingLinksSafely(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	archive := writeArchive(t,
		tar.Header{Name: "index.php", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		// Absolute-target symlink pointing outside the root.
		tar.Header{Name: "escape", Linkname: outside, Typeflag: tar.TypeSymlink},
		// Classic follow-through: a file whose parent is the escaping link.
		tar.Header{Name: "escape/pwned", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		// Hardlink to an absolute system path.
		tar.Header{Name: "hl", Linkname: "/etc/hosts", Typeflag: tar.TypeLink},
		// A safe in-tree relative symlink is preserved.
		tar.Header{Name: "wp-content/object-cache.php", Linkname: "../index.php", Typeflag: tar.TypeSymlink},
	)
	files, _, skipped, err := extractMigrationArchive(archive, dest)
	if err != nil {
		t.Fatalf("safe entries should extract, got error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatal("write escaped the extraction root through a symlink")
	}
	if _, err := os.Lstat(filepath.Join(dest, "index.php")); err != nil {
		t.Fatalf("benign file was not extracted: %v", err)
	}
	// The in-tree symlink is preserved; the two escaping links are skipped.
	if fi, err := os.Lstat(filepath.Join(dest, "wp-content", "object-cache.php")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("safe in-tree symlink not preserved: %v", err)
	}
	if skipped < 2 {
		t.Fatalf("expected escaping links to be skipped, skipped=%d files=%d", skipped, files)
	}
}

// The escaping-link test above uses absolute link targets. Relative ones take a
// different branch: they are resolved against the entry's own directory rather
// than the root, which is where an off-by-one puts a write outside the site.
func TestMigrationArchiveSkipsRelativeEscapingLinks(t *testing.T) {
	outside := t.TempDir()
	dest := filepath.Join(outside, "site")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := writeArchive(t,
		tar.Header{Name: "index.php", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		// Climbs out of the root from the top level.
		tar.Header{Name: "up", Linkname: "../", Typeflag: tar.TypeSymlink},
		// Climbs out from a subdirectory, where the depth arithmetic matters.
		tar.Header{Name: "wp-content/plugins/deep", Linkname: "../../../../", Typeflag: tar.TypeSymlink},
		// Writes attempted through both.
		tar.Header{Name: "up/pwned", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		tar.Header{Name: "wp-content/plugins/deep/pwned", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		// Hardlink whose target climbs out relatively.
		tar.Header{Name: "hl", Linkname: "../../etc/hosts", Typeflag: tar.TypeLink},
	)
	if _, _, skipped, err := extractMigrationArchive(archive, dest); err != nil {
		t.Fatalf("safe entries should extract, got error: %v", err)
	} else if skipped < 3 {
		t.Fatalf("expected the escaping links to be skipped, skipped=%d", skipped)
	}
	for _, p := range []string{
		filepath.Join(outside, "pwned"),
		filepath.Join(filepath.Dir(outside), "pwned"),
	} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("write escaped the extraction root: %s", p)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, "index.php")); err != nil {
		t.Fatalf("benign file was not extracted: %v", err)
	}
}

// An absolute member name is a separate check from a ../ one, and is what a tar
// written on the source host with a full path looks like.
func TestMigrationArchiveRejectsAbsoluteNames(t *testing.T) {
	for _, name := range []string{"/etc/shadow", "//etc/shadow", "/root/.ssh/authorized_keys"} {
		archive := writeArchive(t, tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		if _, _, _, err := extractMigrationArchive(archive, t.TempDir()); err == nil {
			t.Fatalf("absolute name %q was accepted", name)
		}
	}
}

func TestConnectorCopiesMatch(t *testing.T) {
	b, err := os.ReadFile("../../connector/slipstream-connector/slipstream-connector.php")
	if err != nil {
		t.Fatalf("canonical connector missing: %v", err)
	}
	if strings.TrimSpace(string(b)) != strings.TrimSpace(ConnectorPHP) {
		t.Error("connector/slipstream-connector.php is out of sync with agent.ConnectorPHP")
	}
}

func seedCacheEntry(t *testing.T, cacheDir, url string) {
	t.Helper()
	// Mirror velocity.CacheFilePath layout via the package itself to avoid
	// duplicating the hash logic in tests.
	p := cachePathForTest(cacheDir, url)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nginx -V writes its configure line to STDERR and exits 0. A probe that reads
// only stdout gets "" and silently concludes the build has no HTTP/3 — which is
// exactly what shipped and was caught on a real Ubuntu 26.04 box (nginx 1.28,
// --with-http_v3_module) rendering vhosts with no QUIC at all.
func TestNginxHTTP3ProbeReadsStderr(t *testing.T) {
	const configureLine = "nginx version: nginx/1.28.3\nconfigure arguments: --with-http_v3_module --with-http_ssl_module"

	// Combined output carries the configure line; plain stdout is empty, as on
	// a real box.
	m := &mockRunner{outputs: map[string]string{"nginx -V": configureLine}}
	a := &Agent{Runner: m}
	if !a.nginxHasHTTP3() {
		t.Error("expected HTTP/3 to be detected from combined output")
	}

	// A build without the module must not be detected.
	m2 := &mockRunner{outputs: map[string]string{"nginx -V": "configure arguments: --with-http_ssl_module"}}
	a2 := &Agent{Runner: m2}
	if a2.nginxHasHTTP3() {
		t.Error("HTTP/3 must not be reported for a build lacking http_v3_module")
	}

	// The probe must be cached: nginx cannot gain the module without a restart.
	before := len(m.calls)
	a.nginxHasHTTP3()
	if len(m.calls) != before {
		t.Error("nginxHasHTTP3 should probe once and cache the result")
	}
}

// Asking for a PHP version this host does not have used to sail through
// validation and fail deep in provisioning with "Unit php8.4-fpm.service not
// found" — after a Unix user, database and directory tree had been created,
// and with a rollback that tripped over the same missing unit. Caught on a real
// Ubuntu 26.04 box, which ships PHP 8.5 and no 8.4.
func TestCreateSiteRejectsUninstalledPHPVersion(t *testing.T) {
	root := t.TempDir()
	bin := t.TempDir()
	// Both version directories exist — 8.4's is the empty litter a previous
	// failed provision leaves behind — but only 8.5 has an FPM binary.
	for _, v := range []string{"8.4", "8.5"} {
		if err := os.MkdirAll(filepath.Join(root, v, "fpm", "pool.d"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "php-fpm8.5"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		Paths:  Paths{PHPPoolRoot: root, PHPBinDir: bin, SitesRoot: t.TempDir()},
		Runner: &mockRunner{},
	}

	if err := a.validatePHPVersionInstalled("8.4"); err == nil {
		t.Error("expected 8.4 to be rejected on a host that only has 8.5")
	} else if !strings.Contains(err.Error(), "8.5") {
		t.Errorf("error should name the available versions, got: %v", err)
	}
	if err := a.validatePHPVersionInstalled("8.5"); err != nil {
		t.Errorf("8.5 is installed and must be accepted: %v", err)
	}
	// Empty means "panel default" and must not be blocked.
	if err := a.validatePHPVersionInstalled(""); err != nil {
		t.Errorf("empty version must be allowed: %v", err)
	}
	// An unreadable/absent pool root must not block provisioning.
	a2 := &Agent{Paths: Paths{PHPPoolRoot: filepath.Join(root, "nope"), PHPBinDir: bin}, Runner: &mockRunner{}}
	if err := a2.validatePHPVersionInstalled("8.4"); err != nil {
		t.Errorf("must not block when versions cannot be enumerated: %v", err)
	}
}

// The migration import used to hand back a release with no mu-plugins
// directory at all, which silently disabled cache invalidation on every
// imported site. installConnector is what both provisioning and import now
// call; this covers the helper itself, including that it leaves any
// mu-plugins the imported site already had in place.
func TestInstallConnectorAddsMuPluginWithoutClobbering(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "wp-content", "mu-plugins")
	if err := os.MkdirAll(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "theirs.php"), []byte("<?php // theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installConnector(dir); err != nil {
		t.Fatalf("installConnector: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(existing, "slipstream-connector.php"))
	if err != nil {
		t.Fatalf("connector not installed: %v", err)
	}
	if string(got) != ConnectorPHP {
		t.Error("connector contents do not match ConnectorPHP")
	}
	if _, err := os.Stat(filepath.Join(existing, "theirs.php")); err != nil {
		t.Errorf("pre-existing mu-plugin was removed: %v", err)
	}

	// A tree with no wp-content at all is the common import case.
	bare := t.TempDir()
	if err := installConnector(bare); err != nil {
		t.Fatalf("installConnector on bare tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "wp-content", "mu-plugins", "slipstream-connector.php")); err != nil {
		t.Errorf("connector not created in bare tree: %v", err)
	}
}

// Every site used to be handed a quarter of the machine no matter how many
// existed, so four sites each sized themselves for a quarter of RAM and the
// box went to swap with every site looking correctly tuned.
func TestAutoSiteBudgetDividesAcrossSites(t *testing.T) {
	const mem = 2048

	// One site must be exactly what it always was, or this changes tuning
	// for every existing single-site install.
	if got, floored := autoSiteBudgetMB(mem, 1); got != 512 || floored {
		t.Errorf("single site budget = %d, want 512 (unchanged behaviour)", got)
	}
	if got, _ := autoSiteBudgetMB(mem, 0); got != 512 {
		t.Errorf("zero sites should be treated as one, got %d", got)
	}

	if got, floored := autoSiteBudgetMB(mem, 2); got != 256 || floored {
		t.Errorf("two sites = %d, want 256", got)
	}

	// The share never drops below what two PHP workers need, because a site
	// with fewer cannot serve at all.
	got, floored := autoSiteBudgetMB(mem, 100)
	if got != 160 || !floored {
		t.Errorf("floor = %d floored=%v, want 160 true", got, floored)
	}

	// While the floor is not binding, the sites' combined claim must fit the
	// pool. Once it binds the box is over-subscribed and says so, which is the
	// honest outcome: three workers cannot be conjured from 512 MB.
	for _, sites := range []int{1, 2, 3} {
		b, floored := autoSiteBudgetMB(mem, sites)
		if floored {
			continue
		}
		if total := b * sites; total > mem/4 {
			t.Errorf("%d sites claim %d MB, more than the %d MB pool", sites, total, mem/4)
		}
	}
	if _, floored := autoSiteBudgetMB(mem, 4); !floored {
		t.Error("4 sites on 2 GB is over-subscribed and should report it")
	}
}

func TestCountProvisionedSitesIgnoresHalfBuiltTrees(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a.com/releases", "b.com/releases"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// No releases/ yet: provisioning has not finished, so it must not count.
	if err := os.MkdirAll(filepath.Join(root, "c.com"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := countProvisionedSites(root); got != 2 {
		t.Errorf("counted %d sites, want 2", got)
	}
	if got := countProvisionedSites(filepath.Join(root, "nope")); got != 1 {
		t.Errorf("missing root should fall back to 1, got %d", got)
	}
}

// A block theme keeps its header, footer, templates, global styles and
// navigation in posts that have no permalink, no archive and no terms, so the
// per-post purge collapses to the homepage and the feed and every interior
// page keeps serving a stale header. WordPress ships a block theme by default,
// so this is the common case.
//
// This is a tripwire on the connector source rather than a behavioural test:
// the behaviour needs WordPress, and e2e-verify.sh asserts the emitted payload
// on a real site. It still catches the regression that matters, which is
// someone removing a type from the list.
func TestConnectorPurgesSiteWideForBlockThemeObjects(t *testing.T) {
	for _, pt := range []string{"wp_template_part", "wp_template", "wp_global_styles", "wp_navigation"} {
		if !strings.Contains(ConnectorPHP, "'"+pt+"'") {
			t.Errorf("connector does not mention %s, so editing it will not purge the site", pt)
		}
	}
	if !strings.Contains(ConnectorPHP, "SITEWIDE_TYPES") {
		t.Fatal("SITEWIDE_TYPES is gone from the connector")
	}
	// The delegation must happen before the per-post URL set is built, or the
	// site-wide types fall through to it and purge the homepage alone.
	guard := strings.Index(ConnectorPHP, "in_array($post->post_type, self::SITEWIDE_TYPES")
	urls := strings.Index(ConnectorPHP, "$urls = [")
	if guard < 0 || urls < 0 {
		t.Fatal("expected both the site-wide guard and the per-post URL set")
	}
	if guard > urls {
		t.Error("the site-wide guard runs after the per-post URL set, so it never takes effect")
	}
}

// A deploy copies whatever source tree it was given. A build artefact from CI
// contains no mu-plugins, so without an explicit reinstall the promoted
// release has no connector: the site serves 200, nginx still caches, nothing
// is logged, and no purge ever fires again.
// A deploy must leave the release agreeing with the site's recorded object-cache
// setting. The drop-in is a symlink into shared/, and a fresh release directory
// has no such link, so before this it was dropped on the first deploy after
// provisioning while the panel went on reporting the cache as enabled. Found on
// a live server, where a site had been running without one for two weeks.
func TestDeployReleaseKeepsObjectCacheDropinInStep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		seed    bool // a drop-in already in the source tree
		want    bool
	}{
		{"enabled is re-linked into the new release", true, false, true},
		{"enabled survives when the source has one too", true, true, true},
		{"disabled is removed even if the source carried one", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "build")
			if err := os.MkdirAll(filepath.Join(source, "wp-content"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.seed {
				if err := os.WriteFile(filepath.Join(source, "wp-content", "object-cache.php"), []byte("<?php // stale\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			a := &Agent{Runner: &mockRunner{}, Paths: Paths{SitesRoot: root}}
			site := state.Site{
				Domain: "x.com", Type: state.SiteWordPress, SystemUser: "slip-site-9",
				RootPath: filepath.Join(root, "x.com"),
			}
			site.Config.ObjectCache = tc.enabled
			if err := os.MkdirAll(filepath.Join(site.RootPath, "releases"), 0o750); err != nil {
				t.Fatal(err)
			}
			// Provisioning writes the drop-in to shared/; that part already worked.
			if err := os.MkdirAll(filepath.Join(site.RootPath, "shared"), 0o750); err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(site.RootPath, "shared", "object-cache.php")
			if err := os.WriteFile(shared, []byte(APCuDropin), 0o640); err != nil {
				t.Fatal(err)
			}

			if _, err := a.DeployRelease(rpc.DeployParams{Site: site, SourceDir: source, ReleaseID: "20260820-101010"}); err != nil {
				t.Fatalf("deploy: %v", err)
			}

			dropin := filepath.Join(site.RootPath, "releases", "20260820-101010", "wp-content", "object-cache.php")
			fi, err := os.Lstat(dropin)
			switch {
			case tc.want && err != nil:
				t.Fatal("the deployed release has no object-cache drop-in, so the site is running without the cache the panel says it has")
			case !tc.want && err == nil:
				t.Fatal("object cache is off for this site, but the deploy left a drop-in in the release")
			}
			if !tc.want {
				return
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				t.Fatal("the drop-in should be a symlink into shared/, or the next deploy silently forks it")
			}
			target, err := os.Readlink(dropin)
			if err != nil || target != shared {
				t.Fatalf("drop-in points at %q, want the shared copy at %q", target, shared)
			}
		})
	}
}

func TestDeployReleaseInstallsConnectorForWordPress(t *testing.T) {
	for _, tc := range []struct {
		typ  state.SiteType
		want bool
	}{
		{state.SiteWordPress, true},
		{state.SiteWooCommerce, true},
		{state.SiteStatic, false},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "build")
			// A build artefact: site files, deliberately no mu-plugins.
			if err := os.MkdirAll(filepath.Join(source, "wp-content", "themes"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.php"), []byte("<?php\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			a := &Agent{Runner: &mockRunner{}, Paths: Paths{SitesRoot: root}}
			site := state.Site{Domain: "x.com", Type: tc.typ, SystemUser: "slip-site-9", RootPath: filepath.Join(root, "x.com")}
			if err := os.MkdirAll(filepath.Join(site.RootPath, "releases"), 0o750); err != nil {
				t.Fatal(err)
			}
			if _, err := a.DeployRelease(rpc.DeployParams{Site: site, SourceDir: source, ReleaseID: "20260820-101010"}); err != nil {
				t.Fatalf("deploy: %v", err)
			}

			conn := filepath.Join(site.RootPath, "releases", "20260820-101010", "wp-content", "mu-plugins", "slipstream-connector.php")
			_, err := os.Stat(conn)
			if tc.want && err != nil {
				t.Errorf("%s deploy left no connector, so the site would stop invalidating its cache", tc.typ)
			}
			if !tc.want && err == nil {
				t.Errorf("%s is not WordPress and should not get a connector", tc.typ)
			}
		})
	}
}

// The checksums are published in the same release as the binaries, so anyone who
// can write that release can replace both. Provenance is what makes the update
// trustworthy, and it has to fail closed: a box that cannot check is a box that
// does not install.
func TestSelfUpdateRefusesWhenProvenanceCannotBeChecked(t *testing.T) {
	a := &Agent{Runner: &mockRunner{}, Paths: Paths{}}
	staged := map[string]string{"panel-api": "/tmp/panel-api"}

	// No verifier on this host, and nobody said to proceed anyway.
	t.Setenv("PATH", t.TempDir())
	if err := a.verifyProvenance(context.Background(), staged, false); err == nil {
		t.Fatal("an update installed root binaries without checking where they came from")
	}
	// The same box, with the operator explicitly accepting that.
	if err := a.verifyProvenance(context.Background(), staged, true); err != nil {
		t.Fatalf("explicit waiver should proceed: %v", err)
	}
}

// A verifier that runs and rejects the bytes is never waived.
func TestSelfUpdateRefusesWhenProvenanceFails(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	a := &Agent{Runner: &mockRunner{fail: map[string]error{"gh": fmt.Errorf("no attestation found")}}, Paths: Paths{}}
	err := a.verifyProvenance(context.Background(), map[string]string{"panel-api": "/tmp/panel-api"}, true)
	if err == nil {
		t.Fatal("a failed provenance check was waived; it must never be")
	}
}
