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
	mu      sync.Mutex
	calls   []string
	results map[string]any
	errs    map[string]error
}

func (f *fakeAgent) Call(method string, params any, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method)
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
