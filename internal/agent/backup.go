package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

var snapshotIDRe = regexp.MustCompile(`^[a-fA-F0-9]{8,64}$`)

// Backups use Restic: encrypted, deduplicated, off-site. The repository
// password never appears in argv (visible in /proc) — it is passed through
// the environment of each restic invocation.

func (a *Agent) restic(ctx context.Context, password string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	var out, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("restic %s: %w: %s", args[0], err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ensureRepo initializes the repository on first use.
func (a *Agent) ensureRepo(ctx context.Context, repo, password string) error {
	if _, err := a.restic(ctx, password, "-r", repo, "cat", "config"); err == nil {
		return nil
	}
	if _, err := a.restic(ctx, password, "-r", repo, "init"); err != nil {
		return fmt.Errorf("init repository: %w", err)
	}
	return nil
}

// RunBackup snapshots a site's files and/or database into the repository.
func (a *Agent) RunBackup(p rpc.BackupParams) (rpc.BackupResult, error) {
	ctx := context.Background()
	site := p.Site
	if err := validateSite(site); err != nil {
		return rpc.BackupResult{}, err
	}
	if p.Repository == "" || p.Password == "" {
		return rpc.BackupResult{}, fmt.Errorf("repository and password required")
	}
	if err := a.ensureRepo(ctx, p.Repository, p.Password); err != nil {
		return rpc.BackupResult{}, err
	}

	// Database dump joins the file snapshot so one snapshot is a full
	// restore point.
	dumpPath := filepath.Join(site.RootPath, "logs", "db-latest.sql")
	if (p.Kind == "database" || p.Kind == "full") && site.Config.Database.Enabled && !site.Config.Database.External {
		if !dbIdentRe.MatchString(site.Config.Database.Name) {
			return rpc.BackupResult{}, fmt.Errorf("invalid database name")
		}
		out, err := a.Runner.Run(ctx, "mariadb-dump", "--protocol=socket", "--single-transaction", site.Config.Database.Name)
		if err != nil {
			return rpc.BackupResult{}, fmt.Errorf("database dump: %w", err)
		}
		if err := os.WriteFile(dumpPath, []byte(out), 0o600); err != nil {
			return rpc.BackupResult{}, err
		}
	}

	out, err := a.restic(ctx, p.Password, "-r", p.Repository, "backup", site.RootPath,
		"--tag", "site:"+site.Domain, "--tag", "kind:"+p.Kind, "--json")
	if err != nil {
		return rpc.BackupResult{}, err
	}

	// The last JSON line of --json output is the summary.
	var snapshotID string
	var size int64
	for _, line := range strings.Split(out, "\n") {
		var msg struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
			TotalBytes  int64  `json:"total_bytes_processed"`
		}
		if json.Unmarshal([]byte(line), &msg) == nil && msg.MessageType == "summary" {
			snapshotID, size = msg.SnapshotID, msg.TotalBytes
		}
	}
	if snapshotID == "" {
		return rpc.BackupResult{}, fmt.Errorf("backup completed but no snapshot id parsed")
	}
	return rpc.BackupResult{SnapshotID: snapshotID, SizeBytes: size}, nil
}

// TestBackup verifies a repository is reachable and the password is correct,
// initializing it if needed — so an operator learns their backup destination
// works before relying on it, not when they need to restore.
func (a *Agent) TestBackup(p rpc.BackupParams) (map[string]any, error) {
	ctx := context.Background()
	if p.Repository == "" || p.Password == "" {
		return nil, fmt.Errorf("repository and password required")
	}
	if err := a.ensureRepo(ctx, p.Repository, p.Password); err != nil {
		return nil, err
	}
	out, err := a.restic(ctx, p.Password, "-r", p.Repository, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var snaps []struct{ ID string }
	_ = json.Unmarshal([]byte(out), &snaps)
	return map[string]any{"reachable": true, "snapshots": len(snaps)}, nil
}

// RestoreSnapshot restores a snapshot in place (disaster recovery) or into
// a target directory (verified restore tests, cross-server moves).
func (a *Agent) RestoreSnapshot(p rpc.RestoreParams) (map[string]string, error) {
	ctx := context.Background()
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "full"
	}
	if mode != "full" && mode != "files" && mode != "database" {
		return nil, fmt.Errorf("restore mode must be full, files, or database")
	}
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return nil, err
	}
	if !snapshotIDRe.MatchString(p.SnapshotID) {
		return nil, fmt.Errorf("invalid snapshot id")
	}
	target := p.TargetDir
	inPlace := target == ""
	if !inPlace {
		if _, err := a.restic(ctx, p.Password, "-r", p.Repository, "restore", p.SnapshotID, "--target", target); err != nil {
			return nil, err
		}
		return map[string]string{"restored": p.SnapshotID, "target": target}, nil
	}

	scratch := filepath.Join(a.Paths.WorkDir, "restore-"+p.SnapshotID)
	if err := os.RemoveAll(scratch); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if _, err := a.restic(ctx, p.Password, "-r", p.Repository, "restore", p.SnapshotID, "--target", scratch); err != nil {
		return nil, err
	}
	restoredRoot := filepath.Join(scratch, p.Site.RootPath)
	if fi, err := os.Stat(restoredRoot); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("snapshot does not contain site root")
	}
	if mode != "database" {
		currentTarget, err := os.Readlink(filepath.Join(restoredRoot, "current"))
		if err != nil || !strings.HasPrefix(filepath.Clean(currentTarget), filepath.Join(p.Site.RootPath, "releases")+string(os.PathSeparator)) {
			return nil, fmt.Errorf("snapshot has no valid current release")
		}
	}

	managedDB := p.Site.Config.Database.Enabled && !p.Site.Config.Database.External
	restoredDump := filepath.Join(restoredRoot, "logs", "db-latest.sql")
	safetyDB := filepath.Join(a.Paths.WorkDir, "restore-safety-"+p.SnapshotID+".sql")
	defer os.Remove(safetyDB)
	restoreDatabase := func() error {
		if !managedDB {
			return fmt.Errorf("site has no managed database to restore")
		}
		if fi, err := os.Stat(restoredDump); err != nil || fi.IsDir() {
			return fmt.Errorf("snapshot does not contain a database dump")
		}
		if err := a.dumpDatabaseFile(ctx, p.Site.Config.Database.Name, safetyDB); err != nil {
			return fmt.Errorf("create database rollback point: %w", err)
		}
		if err := a.importDatabaseFile(ctx, p.Site.Config.Database.Name, restoredDump); err != nil {
			_ = a.importDatabaseFile(context.Background(), p.Site.Config.Database.Name, safetyDB)
			return fmt.Errorf("database restore: %w", err)
		}
		return nil
	}
	if mode == "database" {
		if err := restoreDatabase(); err != nil {
			return nil, err
		}
		return map[string]string{"restored": p.SnapshotID, "target": p.Site.Config.Database.Name, "mode": mode}, nil
	}

	newRoot := p.Site.RootPath + ".restore-new"
	oldRoot := p.Site.RootPath + ".restore-old"
	os.RemoveAll(newRoot)
	os.RemoveAll(oldRoot)
	if _, err := a.Runner.Run(ctx, "cp", "-a", restoredRoot, newRoot); err != nil {
		return nil, fmt.Errorf("prepare restored files: %w", err)
	}
	if _, err := a.Runner.Run(ctx, "chown", "-R", p.Site.SystemUser+":"+p.Site.SystemUser, newRoot); err != nil {
		return nil, err
	}
	if _, err := a.Runner.Run(ctx, "chown", "root:"+p.Site.SystemUser, newRoot); err != nil {
		return nil, err
	}
	if err := os.Chmod(newRoot, 0o750); err != nil {
		return nil, err
	}

	hasDB := mode == "full" && managedDB
	if hasDB {
		if err := restoreDatabase(); err != nil {
			return nil, err
		}
	}

	rollbackDB := func() {
		if hasDB {
			a.importDatabaseFile(context.Background(), p.Site.Config.Database.Name, safetyDB)
		}
	}
	if err := os.Rename(p.Site.RootPath, oldRoot); err != nil {
		rollbackDB()
		return nil, fmt.Errorf("stage current files: %w", err)
	}
	if err := os.Rename(newRoot, p.Site.RootPath); err != nil {
		os.Rename(oldRoot, p.Site.RootPath)
		rollbackDB()
		return nil, fmt.Errorf("activate restored files: %w", err)
	}
	rollbackFiles := func() {
		os.RemoveAll(newRoot)
		os.Rename(p.Site.RootPath, newRoot)
		os.Rename(oldRoot, p.Site.RootPath)
		rollbackDB()
		if p.Site.PHPVersion != "" {
			a.reloadPHPFPM(context.Background(), p.Site.PHPVersion)
		}
		a.reloadNginx()
	}
	if p.Site.PHPVersion != "" {
		if err := a.reloadPHPFPM(ctx, p.Site.PHPVersion); err != nil {
			rollbackFiles()
			return nil, fmt.Errorf("reload PHP after restore: %w", err)
		}
	}
	if err := a.reloadNginx(); err != nil {
		rollbackFiles()
		return nil, fmt.Errorf("reload nginx after restore: %w", err)
	}
	if _, err := filepath.EvalSymlinks(filepath.Join(p.Site.RootPath, "current")); err != nil {
		rollbackFiles()
		return nil, fmt.Errorf("restored release verification failed: %w", err)
	}
	os.RemoveAll(oldRoot)
	os.RemoveAll(filepath.Join(a.Paths.CacheRoot, p.Site.Domain))
	return map[string]string{"restored": p.SnapshotID, "target": p.Site.RootPath, "mode": mode}, nil
}

func (a *Agent) dumpDatabaseFile(ctx context.Context, database, path string) error {
	if !dbIdentRe.MatchString(database) {
		return fmt.Errorf("invalid database name")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "mariadb-dump", "--protocol=socket", "--single-transaction", database)
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mariadb-dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return f.Sync()
}

func (a *Agent) importDatabaseFile(ctx context.Context, database, path string) error {
	if !dbIdentRe.MatchString(database) {
		return fmt.Errorf("invalid database name")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "mariadb", "--protocol=socket", database)
	cmd.Stdin = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mariadb import: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// VerifyBackup is the feature competitors skip: actually restore the
// snapshot into a scratch directory, check the tree is non-trivial and the
// database dump parses, and time the whole thing so the dashboard can show
// a real recovery estimate.
func (a *Agent) VerifyBackup(p rpc.RestoreParams) (rpc.VerifyResult, error) {
	ctx := context.Background()
	if err := validateSite(p.Site); err != nil {
		return rpc.VerifyResult{}, err
	}
	scratch := filepath.Join(a.Paths.WorkDir, fmt.Sprintf("verify-%s", p.SnapshotID))
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return rpc.VerifyResult{}, err
	}
	defer os.RemoveAll(scratch)

	start := time.Now()
	if _, err := a.restic(ctx, p.Password, "-r", p.Repository, "restore", p.SnapshotID, "--target", scratch); err != nil {
		return rpc.VerifyResult{Passed: false, Detail: "restore failed: " + err.Error()}, nil
	}
	elapsed := time.Since(start).Milliseconds()

	// Repository-level integrity.
	if _, err := a.restic(ctx, p.Password, "-r", p.Repository, "check", "--read-data-subset=10%"); err != nil {
		return rpc.VerifyResult{Passed: false, RestoreMillis: elapsed, Detail: "repository check failed: " + err.Error()}, nil
	}

	// The restored tree must contain the site root with real content.
	restoredRoot := filepath.Join(scratch, p.Site.RootPath)
	var fileCount int
	filepath.Walk(restoredRoot, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Mode().IsRegular() {
			fileCount++
		}
		return nil
	})
	if fileCount == 0 {
		return rpc.VerifyResult{Passed: false, RestoreMillis: elapsed, Detail: "restored tree is empty"}, nil
	}

	// If a database dump is part of the snapshot it must look like SQL.
	dump := filepath.Join(restoredRoot, "logs", "db-latest.sql")
	if b, err := os.ReadFile(dump); err == nil {
		if !strings.Contains(string(b[:min(len(b), 4096)]), "CREATE TABLE") && !strings.Contains(string(b[:min(len(b), 4096)]), "MariaDB dump") {
			return rpc.VerifyResult{Passed: false, RestoreMillis: elapsed, Detail: "database dump looks corrupt"}, nil
		}
	}

	return rpc.VerifyResult{
		Passed:        true,
		RestoreMillis: elapsed,
		Detail:        fmt.Sprintf("%d files restored and verified", fileCount),
	}, nil
}
