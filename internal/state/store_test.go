package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
}

func TestOpenRestrictsDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("state database mode = %04o, want 0640", got)
	}
}

func TestMetricHistoryIsBoundedAndChronological(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 300; i++ {
		if err := s.RecordMetric(MetricSample{CPUHeadroomPct: i, MemHeadroomPct: 80, DiskHeadroomPct: 70}); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := s.ListMetrics(400)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 288 {
		t.Fatalf("retained metrics = %d, want 288", len(metrics))
	}
	if metrics[0].CPUHeadroomPct != 12 || metrics[len(metrics)-1].CPUHeadroomPct != 299 {
		t.Fatalf("metrics not chronological/bounded: first=%d last=%d", metrics[0].CPUHeadroomPct, metrics[len(metrics)-1].CPUHeadroomPct)
	}
}

func TestListUsersIncludesAssignmentsWithoutConnectionDeadlock(t *testing.T) {
	s := testStore(t)
	site, err := s.CreateSite(Site{Domain: "assigned.example.com", Type: SiteStatic, Profile: ProfileBalanced, Engine: EngineNginx, SystemUser: "slip-site-1", RootPath: "/srv/sites/assigned.example.com", Status: SiteActive})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser("operator@example.com", "test-hash", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserSites(user.ID, []int64{site.ID}); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || len(users[0].SiteIDs) != 1 || users[0].SiteIDs[0] != site.ID {
		t.Fatalf("users with assignments = %+v", users)
	}
}

func TestSiteCRUD(t *testing.T) {
	s := testStore(t)

	site, err := s.CreateSite(Site{
		Domain:     "example.com",
		Aliases:    []string{"www.example.com"},
		Type:       SiteWordPress,
		Profile:    ProfileBalanced,
		Engine:     EngineNginx,
		PHPVersion: "8.4",
		SystemUser: "slip-site-1",
		RootPath:   "/srv/sites/example.com",
		Status:     SiteProvisioning,
		Config: SiteConfig{
			CacheEnabled: true,
			Database:     DatabaseConfig{Enabled: true, Host: "127.0.0.1", Port: 3306, Name: "site1", User: "site1"},
			Backups:      BackupPolicy{Enabled: true, Schedule: "hourly", RetentionDays: 30},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if site.ID == 0 {
		t.Fatal("expected assigned ID")
	}

	got, err := s.GetSiteByDomain("example.com")
	if err != nil {
		t.Fatalf("get by domain: %v", err)
	}
	if got.Config.Database.Name != "site1" || !got.Config.CacheEnabled {
		t.Fatalf("config round-trip failed: %+v", got.Config)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "www.example.com" {
		t.Fatalf("aliases round-trip failed: %v", got.Aliases)
	}

	got.Status = SiteActive
	got.Config.ObjectCache = true
	if err := s.UpdateSite(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := s.GetSite(got.ID)
	if got2.Status != SiteActive || !got2.Config.ObjectCache {
		t.Fatalf("update not persisted: %+v", got2)
	}

	// duplicate domain must fail
	if _, err := s.CreateSite(Site{Domain: "example.com", Type: SiteStatic, Profile: ProfileBalanced, Engine: EngineNginx, SystemUser: "x", RootPath: "/x", Status: SiteProvisioning}); err == nil {
		t.Fatal("expected unique-domain violation")
	}

	if err := s.DeleteSite(got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetSite(got.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	s := testStore(t)
	task, err := s.CreateTask("site.create", 1)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.StartTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ProgressTask(task.ID, 40, "creating database"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTask(task.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TaskSucceeded || got.Progress != 100 {
		t.Fatalf("unexpected task state: %+v", got)
	}
	if got.Log == "" || got.FinishedAt == nil {
		t.Fatalf("expected log and finish time: %+v", got)
	}
}

func TestSessionsAndSetupToken(t *testing.T) {
	s := testStore(t)
	u, err := s.CreateUser("admin@example.com", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.CreateSession("tok123", u.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserForSession("tok123")
	if err != nil || got.Email != "admin@example.com" {
		t.Fatalf("session lookup: %v %+v", err, got)
	}
	if _, err := s.UserForSession("wrong"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// expired session
	if err := s.CreateSession("old", u.ID, -time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserForSession("old"); err != ErrNotFound {
		t.Fatalf("expected expiry, got %v", err)
	}

	// setup token is single-use
	if err := s.CreateSetupToken("setup1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeSetupToken("setup1"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := s.ConsumeSetupToken("setup1"); err != ErrNotFound {
		t.Fatalf("expected single-use, got %v", err)
	}
}

func TestDeploymentsBackupsDrift(t *testing.T) {
	s := testStore(t)
	site, err := s.CreateSite(Site{Domain: "d.com", Type: SitePHP, Profile: ProfileBalanced, Engine: EngineNginx, SystemUser: "u", RootPath: "/srv/sites/d.com", Status: SiteActive})
	if err != nil {
		t.Fatal(err)
	}

	d, err := s.CreateDeployment(Deployment{SiteID: site.ID, ReleaseID: "20260711-120000", Path: "/srv/sites/d.com/releases/20260711-120000", Status: DeployCreated})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeploymentStatus(d.ID, DeployPromoted, `{"verdict":"pass"}`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDeployment(d.ID)
	if got.Status != DeployPromoted || got.PromotedAt == nil || got.GuardJSON == "" {
		t.Fatalf("deployment state: %+v", got)
	}

	b, err := s.CreateBackup(Backup{SiteID: site.ID, SnapshotID: "abc123", Repository: "primary", Kind: "full", SizeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBackupVerified(b.ID, VerifyPassed, 222000); err != nil {
		t.Fatal(err)
	}
	backups, _ := s.ListBackups(site.ID, 10)
	if len(backups) != 1 || backups[0].VerifyStatus != VerifyPassed || backups[0].RestoreEstMS != 222000 {
		t.Fatalf("backup state: %+v", backups)
	}

	if err := s.RecordManagedFile("/etc/nginx/sites-enabled/d.com.conf", "aaaa"); err != nil {
		t.Fatal(err)
	}
	files, _ := s.ManagedFiles()
	if files["/etc/nginx/sites-enabled/d.com.conf"] != "aaaa" {
		t.Fatalf("managed files: %v", files)
	}
	ev, err := s.CreateDriftEvent(DriftEvent{Path: "/etc/nginx/sites-enabled/d.com.conf", ExpectedHash: "aaaa", ActualHash: "bbbb"})
	if err != nil {
		t.Fatal(err)
	}
	open, _ := s.ListDriftEvents(false)
	if len(open) != 1 {
		t.Fatalf("expected 1 open drift, got %d", len(open))
	}
	if err := s.ResolveDriftEvent(ev.ID, DriftRestored); err != nil {
		t.Fatal(err)
	}
	open, _ = s.ListDriftEvents(false)
	if len(open) != 0 {
		t.Fatalf("expected 0 open drift, got %d", len(open))
	}

	// cascade delete
	if err := s.DeleteSite(site.ID); err != nil {
		t.Fatal(err)
	}
	deps, _ := s.ListDeployments(site.ID, 10)
	if len(deps) != 0 {
		t.Fatal("expected deployments cascade-deleted")
	}
}
