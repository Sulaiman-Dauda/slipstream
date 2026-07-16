package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// fakeAgent implements AgentCaller with canned results per method.
type fakeAgent struct {
	mu         sync.Mutex
	calls      []string
	lastParams map[string]any
	results    map[string]any
	errs       map[string]error
}

func (f *fakeAgent) Call(method string, params any, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	if f.lastParams == nil {
		f.lastParams = map[string]any{}
	}
	f.lastParams[method] = params
	f.mu.Unlock()
	if err := f.errs[method]; err != nil {
		return err
	}
	if res, ok := f.results[method]; ok && out != nil {
		b, _ := json.Marshal(res)
		return json.Unmarshal(b, out)
	}
	return nil
}

func (f *fakeAgent) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == method {
			return true
		}
	}
	return false
}

func testServer(t *testing.T) (*Server, *fakeAgent, *httptest.Server) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	agent := &fakeAgent{
		results: map[string]any{
			rpc.MethodCreateSite: rpc.CreateSiteResult{
				SystemUser: "slip-site-1", RootPath: "/srv/sites/example.com",
				Files: []rpc.ManagedFile{{Path: "/etc/nginx/sites-enabled/example.com.conf", SHA256: "abc"}},
			},
			rpc.MethodPurgeCache: rpc.PurgeResult{Removed: 2},
		},
		errs: map[string]error{},
	}
	s := &Server{Store: store, Agent: agent, Log: slog.Default(), InsecureCookies: true}
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(ts.Close)
	return s, agent, ts
}

// client wraps cookie-carrying requests.
type client struct {
	t       *testing.T
	base    string
	cookies []*http.Cookie
}

func (c *client) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if cs := resp.Cookies(); len(cs) > 0 {
		c.cookies = cs
	}
	return resp, buf.Bytes()
}

func setupAdmin(t *testing.T, s *Server, ts *httptest.Server) *client {
	t.Helper()
	if err := s.Store.CreateSetupToken("boot-token", time.Hour); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: ts.URL}
	resp, body := c.do("POST", "/api/setup", map[string]string{
		"email": "admin@example.com", "password": "a-long-password!", "token": "boot-token",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, body)
	}
	return c
}

func TestSetupAndAuthFlow(t *testing.T) {
	s, _, ts := testServer(t)

	// API is locked before login.
	anon := &client{t: t, base: ts.URL}
	if resp, _ := anon.do("GET", "/api/sites", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated sites = %d, want 401", resp.StatusCode)
	}

	c := setupAdmin(t, s, ts)

	// Setup token is single-use.
	fresh := &client{t: t, base: ts.URL}
	if resp, _ := fresh.do("POST", "/api/setup", map[string]string{
		"email": "x@example.com", "password": "another-password!", "token": "boot-token",
	}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", resp.StatusCode)
	}

	// Session works.
	if resp, _ := c.do("GET", "/api/sites", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("authed sites = %d, want 200", resp.StatusCode)
	}

	// Wrong password refused.
	bad := &client{t: t, base: ts.URL}
	if resp, _ := bad.do("POST", "/api/login", map[string]string{
		"email": "admin@example.com", "password": "wrong-password!!",
	}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", resp.StatusCode)
	}

	// Right password succeeds.
	good := &client{t: t, base: ts.URL}
	if resp, _ := good.do("POST", "/api/login", map[string]string{
		"email": "admin@example.com", "password": "a-long-password!",
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}
}

func TestConnectorRoutesExposeOnlyConnectorSurface(t *testing.T) {
	s, _, _ := testServer(t)
	ts := httptest.NewServer(s.ConnectorRoutes())
	defer ts.Close()

	for _, path := range []string{"/", "/api/bootstrap", "/api/login", "/api/sites"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want restricted route", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}
}

func waitForTasks(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := s.Store.ListTasks(50)
		if err != nil {
			t.Fatal(err)
		}
		busy := false
		for _, task := range tasks {
			if task.Status == state.TaskPending || task.Status == state.TaskRunning {
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tasks did not finish")
}

func TestCreateSiteFlow(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)

	resp, body := c.do("POST", "/api/sites", map[string]any{
		"domain": "example.com", "type": "wordpress",
		"admin_email": "owner@example.com", "admin_user": "owner",
		"admin_password": "wordpress-admin-pass",
		"title":          "Example",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)

	if !agent.called(rpc.MethodCreateSite) {
		t.Fatal("agent CreateSite was not called")
	}
	site, err := s.Store.GetSiteByDomain("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if site.Status != state.SiteActive {
		tasks, _ := s.Store.ListTasks(5)
		t.Fatalf("site status = %s; tasks: %+v", site.Status, tasks)
	}
	if site.SystemUser != fmt.Sprintf("slip-site-%d", site.ID) {
		t.Errorf("system user = %s", site.SystemUser)
	}
	if site.Config.Database.Name != fmt.Sprintf("site_%d", site.ID) {
		t.Errorf("db name = %s", site.Config.Database.Name)
	}
	// Managed file recorded for drift detection.
	files, _ := s.Store.ManagedFiles()
	if files["/etc/nginx/sites-enabled/example.com.conf"] != "abc" {
		t.Errorf("managed files = %v", files)
	}
	// Duplicate domain conflicts.
	if resp, _ := c.do("POST", "/api/sites", map[string]any{
		"domain": "example.com", "type": "static",
	}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", resp.StatusCode)
	}
	// Invalid domain rejected.
	if resp, _ := c.do("POST", "/api/sites", map[string]any{
		"domain": "bad domain!", "type": "static",
	}); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("expected invalid domain rejection")
	}
}

func TestConnectorPurgeAuth(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)

	resp, _ := c.do("POST", "/api/sites", map[string]any{"domain": "blog.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("site create failed")
	}
	waitForTasks(t, s)
	site, _ := s.Store.GetSiteByDomain("blog.example.com")
	token, _ := s.Store.GetSetting(secretKey(site.ID, "connector_token"), "")
	if token == "" {
		t.Fatal("connector token missing")
	}

	purge := func(auth string, body map[string]any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", ts.URL+"/api/connector/purge", bytes.NewReader(b))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := purge("", map[string]any{"all": true}); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", code)
	}
	if code := purge("wrong", map[string]any{"all": true}); code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d", code)
	}
	if code := purge(token, map[string]any{"urls": []string{"https://blog.example.com/post/"}}); code != http.StatusOK {
		t.Fatalf("valid purge = %d", code)
	}
	if !agent.called(rpc.MethodPurgeCache) {
		t.Fatal("purge not forwarded to agent")
	}

	// Foreign URLs are dropped, not purged.
	agent.mu.Lock()
	agent.calls = nil
	agent.mu.Unlock()
	if code := purge(token, map[string]any{"urls": []string{"https://victim.example.net/"}}); code != http.StatusOK {
		t.Fatal("foreign purge should 200 with zero removals")
	}
	if agent.called(rpc.MethodPurgeCache) {
		t.Fatal("foreign URL must not reach the agent")
	}
}

func TestDeleteFailurePreservesDesiredState(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "keep.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("keep.example.com")
	if err != nil {
		t.Fatal(err)
	}
	agent.errs[rpc.MethodDeleteSite] = fmt.Errorf("simulated cleanup failure")
	resp, body = c.do("DELETE", fmt.Sprintf("/api/sites/%d", site.ID), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	kept, err := s.Store.GetSite(site.ID)
	if err != nil {
		t.Fatalf("failed deletion removed desired state: %v", err)
	}
	if kept.Status != state.SiteError {
		t.Fatalf("site status = %s, want error", kept.Status)
	}
}

func TestCreateFailureRollsBackStateAndAllowsRetry(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	agent.errs[rpc.MethodCreateSite] = fmt.Errorf("simulated provisioning failure")
	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "retry.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	if _, err := s.Store.GetSiteByDomain("retry.example.com"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("failed provisioning retained partial site: %v", err)
	}
	delete(agent.errs, rpc.MethodCreateSite)
	resp, body = c.do("POST", "/api/sites", map[string]any{"domain": "retry.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry create = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	if _, err := s.Store.GetSiteByDomain("retry.example.com"); err != nil {
		t.Fatalf("retry did not create site: %v", err)
	}
}

func TestOperatorIsRestrictedToAssignedSites(t *testing.T) {
	s, _, ts := testServer(t)
	admin := setupAdmin(t, s, ts)
	for _, domain := range []string{"assigned.example.com", "private.example.com"} {
		resp, body := admin.do("POST", "/api/sites", map[string]any{"domain": domain, "type": "static"})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("create %s = %d %s", domain, resp.StatusCode, body)
		}
		waitForTasks(t, s)
	}
	assigned, _ := s.Store.GetSiteByDomain("assigned.example.com")
	resp, body := admin.do("POST", "/api/users", map[string]any{
		"email": "operator@example.com", "password": "operator-password!", "role": "operator", "site_ids": []int64{assigned.ID},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create operator = %d %s", resp.StatusCode, body)
	}
	op := &client{t: t, base: ts.URL}
	resp, body = op.do("POST", "/api/login", map[string]string{"email": "operator@example.com", "password": "operator-password!"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator login = %d %s", resp.StatusCode, body)
	}
	resp, body = op.do("GET", "/api/sites", nil)
	var visible []state.Site
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &visible) != nil || len(visible) != 1 || visible[0].ID != assigned.ID {
		t.Fatalf("operator sites = %d %s", resp.StatusCode, body)
	}
	private, _ := s.Store.GetSiteByDomain("private.example.com")
	if resp, _ := op.do("GET", fmt.Sprintf("/api/sites/%d", private.ID), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unassigned site = %d, want 404", resp.StatusCode)
	}
	if resp, _ := op.do("POST", fmt.Sprintf("/api/sites/%d/purge", assigned.ID), map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("assigned mutation = %d", resp.StatusCode)
	}
	if resp, _ := op.do("GET", "/api/users", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("global users = %d, want 403", resp.StatusCode)
	}
	// Global log sources cover every site plus the panel's own control-plane
	// traffic. An operator must never read them, even by pairing the global
	// source with a "site" query param they legitimately own.
	for _, src := range []string{"nginx-access", "nginx-error", "mariadb", "agent", "api", "php-error"} {
		resp, body := op.do("GET", fmt.Sprintf("/api/logs?source=%s&site=assigned.example.com", src), nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("operator read global log %q = %d %s, want 403", src, resp.StatusCode, body)
		}
	}
	// A site-scoped source naming a DIFFERENT site than the one just proven
	// accessible must not slip through either.
	resp, body = op.do("GET", "/api/logs?source=site:private.example.com:access&site=assigned.example.com", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("mismatched site/source = %d %s, want 404", resp.StatusCode, body)
	}
	// The operator's own site log is still allowed.
	if resp, _ := op.do("GET", "/api/logs?source=site:assigned.example.com:access&site=assigned.example.com", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("own site log = %d, want 200", resp.StatusCode)
	}
}

func TestRestoreRequiresConfirmationAndSafetySnapshot(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	resp, _ := c.do("POST", "/api/sites", map[string]any{"domain": "restore.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("site create failed")
	}
	waitForTasks(t, s)
	site, _ := s.Store.GetSiteByDomain("restore.example.com")
	s.Store.SetSetting("backup_repository", "/tmp/test-repo")
	s.Store.SetSetting("backup_password", "test-repository-password")
	backup, err := s.Store.CreateBackup(state.Backup{SiteID: site.ID, SnapshotID: "abcdef1234567890", Repository: "/tmp/test-repo", Kind: "full"})
	if err != nil {
		t.Fatal(err)
	}
	agent.results[rpc.MethodRunBackup] = rpc.BackupResult{SnapshotID: "fedcba0987654321", SizeBytes: 42}
	if resp, _ := c.do("POST", fmt.Sprintf("/api/backups/%d/restore", backup.ID), map[string]string{"confirm": "wrong"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong confirmation = %d", resp.StatusCode)
	}
	if resp, _ := c.do("POST", fmt.Sprintf("/api/backups/%d/restore", backup.ID), map[string]string{"confirm": site.Domain, "mode": "everything"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid restore mode = %d", resp.StatusCode)
	}
	resp, body := c.do("POST", fmt.Sprintf("/api/backups/%d/restore", backup.ID), map[string]string{"confirm": site.Domain})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restore = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	if !agent.called(rpc.MethodRunBackup) || !agent.called(rpc.MethodRestoreSnapshot) {
		t.Fatalf("restore did not create safety snapshot and restore: %v", agent.calls)
	}
	backups, _ := s.Store.ListBackups(site.ID, 10)
	foundSafety := false
	for _, b := range backups {
		if b.SnapshotID == "fedcba0987654321" && b.Kind == "pre-restore" {
			foundSafety = true
		}
	}
	if !foundSafety {
		t.Fatal("pre-restore safety snapshot was not recorded")
	}
}

func TestDatabaseImportRequiresDomainConfirmation(t *testing.T) {
	s, _, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	resp, _ := c.do("POST", "/api/sites", map[string]any{
		"domain": "dbimport.example.com", "type": "wordpress", "title": "Import test",
		"admin_email": "owner@dbimport.example.com", "admin_user": "owner", "admin_password": "wordpress-admin-pass",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("site create failed")
	}
	waitForTasks(t, s)
	site, _ := s.Store.GetSiteByDomain("dbimport.example.com")
	resp, _ = c.do("POST", fmt.Sprintf("/api/sites/%d/database/import", site.ID), map[string]string{
		"path": "shared/import.sql", "confirm": "wrong.example.com",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong import confirmation = %d", resp.StatusCode)
	}
}

func TestShellQuotePreservesSingleQuotes(t *testing.T) {
	if got, want := shellQuote("echo 'safe'"), `'echo '"'"'safe'"'"''`; got != want {
		t.Fatalf("shellQuote = %q, want %q", got, want)
	}
}

// When ApplySiteConfig fails (e.g. a php-fpm unit that doesn't exist on this
// host), the stored site record must revert to the pre-request values instead
// of permanently claiming a PHP version/limits that were never actually
// applied by the agent.
func TestPHPSettingsRevertOnAgentFailure(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "phprevert.example.com", "type": "php", "php_version": "8.4"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("phprevert.example.com")
	if err != nil {
		t.Fatal(err)
	}

	agent.errs[rpc.MethodApplySiteConfig] = fmt.Errorf("systemctl reload php8.3-fpm: unit not found")
	resp, body = c.do("PUT", fmt.Sprintf("/api/sites/%d/php", site.ID), map[string]any{"php_version": "8.3", "memory_limit_mb": 512})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("php settings request = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)

	after, err := s.Store.GetSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PHPVersion != "8.4" {
		t.Errorf("php_version = %q after failed apply, want reverted to 8.4", after.PHPVersion)
	}
	if after.Config.PHP.MemoryLimitMB != 0 {
		t.Errorf("memory_limit_mb = %d after failed apply, want reverted to 0", after.Config.PHP.MemoryLimitMB)
	}

	delete(agent.errs, rpc.MethodApplySiteConfig)
	resp, body = c.do("PUT", fmt.Sprintf("/api/sites/%d/php", site.ID), map[string]any{"php_version": "8.3"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("retry php settings = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	after, _ = s.Store.GetSite(site.ID)
	if after.PHPVersion != "8.3" {
		t.Errorf("retry php_version = %q, want 8.3 applied", after.PHPVersion)
	}
}

// Same reversion guarantee for the general site-config endpoint (cache,
// object cache, profile, php_workers).
func TestUpdateSiteConfigRevertOnAgentFailure(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)
	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "configrevert.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("configrevert.example.com")
	if err != nil {
		t.Fatal(err)
	}
	before := site.Config.CacheEnabled

	agent.errs[rpc.MethodApplySiteConfig] = fmt.Errorf("simulated render failure")
	resp, body = c.do("PUT", fmt.Sprintf("/api/sites/%d/config", site.ID), map[string]any{"cache_enabled": !before})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("update config = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)

	after, err := s.Store.GetSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Config.CacheEnabled != before {
		t.Errorf("cache_enabled = %v after failed apply, want reverted to %v", after.Config.CacheEnabled, before)
	}
}

// Deleting something must leave the audit trail able to say WHAT was
// deleted, not just who deleted something and when.
func TestAuditRecordsDeletionTargets(t *testing.T) {
	s, _, ts := testServer(t)
	c := setupAdmin(t, s, ts)

	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "audit.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("audit.example.com")
	if err != nil {
		t.Fatal(err)
	}

	resp, body = c.do("POST", fmt.Sprintf("/api/sites/%d/cron", site.ID), map[string]string{
		"schedule": "*/15 * * * *", "command": "wp cron event run --due-now",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create cron = %d %s", resp.StatusCode, body)
	}
	var job state.CronJob
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	if resp, body := c.do("DELETE", fmt.Sprintf("/api/cron/%d", job.ID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete cron = %d %s", resp.StatusCode, body)
	}

	resp, body = c.do("POST", "/api/users", map[string]any{
		"email": "todelete@example.com", "password": "a-long-password!", "role": "readonly",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create user = %d %s", resp.StatusCode, body)
	}
	var created state.User
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if resp, body := c.do("DELETE", fmt.Sprintf("/api/users/%d", created.ID), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete user = %d %s", resp.StatusCode, body)
	}

	events, err := s.Store.ListAuditEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	var sawCronDelete, sawUserDelete bool
	for _, e := range events {
		if e.Action == "cron.delete" {
			sawCronDelete = true
			if e.Subject != "audit.example.com" || !strings.Contains(e.Detail, "wp cron event run") {
				t.Errorf("cron.delete event = subject:%q detail:%q, want site domain + command", e.Subject, e.Detail)
			}
		}
		if e.Action == "user.delete" {
			sawUserDelete = true
			if e.Subject != "todelete@example.com" {
				t.Errorf("user.delete event subject = %q, want the deleted email", e.Subject)
			}
		}
	}
	if !sawCronDelete {
		t.Error("no cron.delete audit event found")
	}
	if !sawUserDelete {
		t.Error("no user.delete audit event found")
	}
}

// crontab(5): an unescaped "%" in a job's command field becomes a newline,
// and everything after the first one is fed to the command as stdin instead
// of running. Our own timestamp-wrapper printf format strings contain
// literal %s, so the rendered crontab must never reach WriteCrontab with an
// unescaped "%" anywhere in a job line -- that silently truncated every
// rendered cron job (confirmed live: cron fired every minute per syslog, but
// the wrapped command never actually ran).
func TestRenderedCrontabEscapesPercent(t *testing.T) {
	s, agent, ts := testServer(t)
	c := setupAdmin(t, s, ts)

	resp, body := c.do("POST", "/api/sites", map[string]any{"domain": "crontest.example.com", "type": "static"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create site = %d %s", resp.StatusCode, body)
	}
	waitForTasks(t, s)
	site, err := s.Store.GetSiteByDomain("crontest.example.com")
	if err != nil {
		t.Fatal(err)
	}

	resp, body = c.do("POST", fmt.Sprintf("/api/sites/%d/cron", site.ID), map[string]string{
		"schedule": "*/5 * * * *", "command": "echo 'date fmt %Y-%m-%d also 100% done'",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create cron = %d %s", resp.StatusCode, body)
	}

	agent.mu.Lock()
	params, ok := agent.lastParams[rpc.MethodWriteCrontab].(rpc.CrontabParams)
	agent.mu.Unlock()
	if !ok {
		t.Fatal("WriteCrontab was not called with CrontabParams")
	}
	for i, line := range strings.Split(params.Content, "\n") {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "PATH=") || line == "" {
			continue
		}
		// Walk the line looking for a "%" not preceded by a backslash.
		for j := 0; j < len(line); j++ {
			if line[j] == '%' && (j == 0 || line[j-1] != '\\') {
				t.Fatalf("line %d has an unescaped %%, cron would truncate the job here: %q", i, line)
			}
		}
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s, _, ts := testServer(t)
	c := setupAdmin(t, s, ts)

	resp, _ := c.do("PUT", "/api/settings", map[string]string{
		"acme_email": "ops@example.com", "backup_password": "supersecret",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put settings = %d", resp.StatusCode)
	}
	_, body := c.do("GET", "/api/settings", nil)
	var got map[string]string
	json.Unmarshal(body, &got)
	if got["acme_email"] != "ops@example.com" {
		t.Errorf("acme_email = %q", got["acme_email"])
	}
	if strings.Contains(got["backup_password"], "supersecret") {
		t.Error("backup password must be masked in responses")
	}
	// Unknown key rejected.
	if resp, _ := c.do("PUT", "/api/settings", map[string]string{"evil": "x"}); resp.StatusCode != http.StatusBadRequest {
		t.Error("unknown setting must be rejected")
	}
	stored, _ := s.Store.GetSetting("backup_password", "")
	if stored != "supersecret" {
		t.Errorf("stored password = %q", stored)
	}
}
