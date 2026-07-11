package agent

import (
	"context"
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

func TestCreateSiteRejectsBadInput(t *testing.T) {
	a, run := testAgent(t)
	bad := []state.Site{
		{Domain: "evil.com; }", SystemUser: "slip-site-1"},
		{Domain: "ok.example.com", SystemUser: "root"},
		{Domain: "ok.example.com", SystemUser: "slip-site-1", PHPVersion: "7.4; rm -rf /"},
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
