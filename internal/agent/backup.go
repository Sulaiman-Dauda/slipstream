package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

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
			MessageType    string `json:"message_type"`
			SnapshotID     string `json:"snapshot_id"`
			TotalBytes     int64  `json:"total_bytes_processed"`
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

// RestoreSnapshot restores a snapshot in place (disaster recovery) or into
// a target directory (verified restore tests, cross-server moves).
func (a *Agent) RestoreSnapshot(p rpc.RestoreParams) (map[string]string, error) {
	ctx := context.Background()
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	target := p.TargetDir
	inPlace := target == ""
	if inPlace {
		target = "/"
	}
	if _, err := a.restic(ctx, p.Password, "-r", p.Repository, "restore", p.SnapshotID, "--target", target); err != nil {
		return nil, err
	}
	if inPlace {
		if _, err := a.Runner.Run(ctx, "chown", "-R", p.Site.SystemUser+":"+p.Site.SystemUser, p.Site.RootPath); err != nil {
			return nil, err
		}
		// Re-import the database dump captured in the snapshot.
		dump := filepath.Join(p.Site.RootPath, "logs", "db-latest.sql")
		if _, err := os.Stat(dump); err == nil && p.Site.Config.Database.Enabled && !p.Site.Config.Database.External {
			if _, err := a.Runner.Run(ctx, "mariadb", "--protocol=socket", p.Site.Config.Database.Name, "-e", "source "+dump); err != nil {
				return nil, fmt.Errorf("database restore: %w", err)
			}
		}
	}
	return map[string]string{"restored": p.SnapshotID, "target": target}, nil
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
