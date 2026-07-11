package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

const maxMigrationBytes = int64(20 << 30)
const maxMigrationFiles = 200000

// ImportMigration safely expands a site archive, creates an immutable
// release, optionally imports its database, rewrites the WordPress URL, and
// rolls both files and database back if any activation step fails.
func (a *Agent) ImportMigration(p rpc.MigrationParams) (rpc.MigrationResult, error) {
	if err := validateSite(p.Site); err != nil {
		return rpc.MigrationResult{}, err
	}
	if err := a.validateSiteRoot(p.Site); err != nil {
		return rpc.MigrationResult{}, err
	}
	if !releaseIDRe.MatchString(p.ReleaseID) || p.ReleaseID == "initial" {
		return rpc.MigrationResult{}, fmt.Errorf("invalid migration release id")
	}
	if p.OldDomain != "" {
		if err := nginx.ValidateDomain(p.OldDomain); err != nil {
			return rpc.MigrationResult{}, fmt.Errorf("invalid old domain: %w", err)
		}
	}
	archivePath, err := resolveInSite(p.Site.RootPath, p.Archive)
	if err != nil {
		return rpc.MigrationResult{}, err
	}
	if !strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") && !strings.HasSuffix(strings.ToLower(archivePath), ".tgz") {
		return rpc.MigrationResult{}, fmt.Errorf("migration archive must be .tar.gz or .tgz")
	}
	work := filepath.Join(a.Paths.WorkDir, "migration-"+p.ReleaseID)
	if err := os.RemoveAll(work); err != nil {
		return rpc.MigrationResult{}, err
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return rpc.MigrationResult{}, err
	}
	defer os.RemoveAll(work)
	files, size, err := extractMigrationArchive(archivePath, work)
	if err != nil {
		return rpc.MigrationResult{}, err
	}
	source, err := migrationSourceRoot(work)
	if err != nil {
		return rpc.MigrationResult{}, err
	}
	// WordPress uploads are persistent shared data rather than release data.
	// Stage them separately so activation can swap them atomically and roll
	// them back together with code and database.
	var newUploads, oldUploads string
	uploadsSwapped := false
	archiveUploads := filepath.Join(source, "wp-content", "uploads")
	if fi, statErr := os.Stat(archiveUploads); statErr == nil && fi.IsDir() {
		newUploads = filepath.Join(p.Site.RootPath, "shared", "uploads.migration-new")
		oldUploads = filepath.Join(p.Site.RootPath, "shared", "uploads.migration-old")
		_ = os.RemoveAll(newUploads)
		_ = os.RemoveAll(oldUploads)
		if _, err := a.Runner.Run(context.Background(), "cp", "-a", archiveUploads, newUploads); err != nil {
			return rpc.MigrationResult{}, fmt.Errorf("stage uploads: %w", err)
		}
		if _, err := a.Runner.Run(context.Background(), "chown", "-R", p.Site.SystemUser+":"+p.Site.SystemUser, newUploads); err != nil {
			return rpc.MigrationResult{}, err
		}
		defer os.RemoveAll(newUploads)
	}
	dep, err := a.DeployRelease(rpc.DeployParams{Site: p.Site, SourceDir: source, ReleaseID: p.ReleaseID})
	if err != nil {
		return rpc.MigrationResult{}, err
	}
	cleanupRelease := true
	defer func() {
		if cleanupRelease {
			_ = os.RemoveAll(dep.Path)
		}
	}()

	ctx := context.Background()
	oldCurrent, err := os.Readlink(filepath.Join(p.Site.RootPath, "current"))
	if err != nil {
		return rpc.MigrationResult{}, err
	}
	var safetyDB string
	if p.SQL != "" {
		if !p.Site.Config.Database.Enabled || p.Site.Config.Database.External {
			return rpc.MigrationResult{}, fmt.Errorf("site has no local managed database")
		}
		sqlPath, err := resolveInSite(p.Site.RootPath, p.SQL)
		if err != nil || !strings.HasSuffix(strings.ToLower(p.SQL), ".sql") {
			return rpc.MigrationResult{}, fmt.Errorf("invalid SQL import path")
		}
		safetyDB = filepath.Join(work, "database-safety.sql")
		if err := a.dumpDatabaseFile(ctx, p.Site.Config.Database.Name, safetyDB); err != nil {
			return rpc.MigrationResult{}, fmt.Errorf("create database rollback point: %w", err)
		}
		if err := a.importDatabaseFile(ctx, p.Site.Config.Database.Name, sqlPath); err != nil {
			_ = a.importDatabaseFile(context.Background(), p.Site.Config.Database.Name, safetyDB)
			return rpc.MigrationResult{}, fmt.Errorf("database import failed and was rolled back: %w", err)
		}
	}
	rollback := func() {
		_ = forceSymlink(oldCurrent, filepath.Join(p.Site.RootPath, "current"))
		if uploadsSwapped {
			_ = os.RemoveAll(filepath.Join(p.Site.RootPath, "shared", "uploads"))
			if err := os.Rename(oldUploads, filepath.Join(p.Site.RootPath, "shared", "uploads")); os.IsNotExist(err) {
				_ = os.MkdirAll(filepath.Join(p.Site.RootPath, "shared", "uploads"), 0o750)
			}
		}
		if safetyDB != "" {
			_ = a.importDatabaseFile(context.Background(), p.Site.Config.Database.Name, safetyDB)
		}
		if p.Site.PHPVersion != "" {
			_ = a.reloadPHPFPM(context.Background(), p.Site.PHPVersion)
		}
	}
	if newUploads != "" {
		currentUploads := filepath.Join(p.Site.RootPath, "shared", "uploads")
		if err := os.Rename(currentUploads, oldUploads); err != nil && !os.IsNotExist(err) {
			return rpc.MigrationResult{}, fmt.Errorf("stage current uploads: %w", err)
		}
		if err := os.Rename(newUploads, currentUploads); err != nil {
			_ = os.Rename(oldUploads, currentUploads)
			rollback()
			return rpc.MigrationResult{}, fmt.Errorf("activate migrated uploads: %w", err)
		}
		uploadsSwapped = true
	}
	if _, err := a.PromoteRelease(rpc.ReleaseParams{Site: p.Site, ReleaseID: p.ReleaseID}); err != nil {
		rollback()
		return rpc.MigrationResult{}, err
	}
	if oldUploads != "" {
		_ = os.RemoveAll(oldUploads)
	}
	if p.OldDomain != "" && (p.Site.Type == state.SiteWordPress || p.Site.Type == state.SiteWooCommerce) {
		if _, err := a.wp(ctx, p.Site, "search-replace", "https://"+p.OldDomain, "https://"+p.Site.Domain, "--all-tables-with-prefix", "--precise", "--skip-columns=guid"); err != nil {
			rollback()
			return rpc.MigrationResult{}, fmt.Errorf("URL rewrite failed; migration rolled back: %w", err)
		}
	}
	cleanupRelease = false
	return rpc.MigrationResult{ReleaseID: p.ReleaseID, Files: files, Bytes: size}, nil
}

func extractMigrationArchive(path, dest string) (int, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, fmt.Errorf("open migration archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files int
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, total, err
		}
		name := filepath.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return files, total, fmt.Errorf("archive path escapes destination")
		}
		target := filepath.Join(dest, name)
		if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			return files, total, fmt.Errorf("archive path escapes destination")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, total, err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			total += h.Size
			if files > maxMigrationFiles || total > maxMigrationBytes {
				return files, total, fmt.Errorf("migration archive exceeds safety limits")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, total, err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return files, total, err
			}
			_, copyErr := io.CopyN(out, tr, h.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return files, total, copyErr
			}
			if closeErr != nil {
				return files, total, closeErr
			}
		default:
			return files, total, fmt.Errorf("archive contains unsupported link or special file %q", h.Name)
		}
	}
	if files == 0 {
		return 0, 0, fmt.Errorf("migration archive is empty")
	}
	return files, total, nil
}

func migrationSourceRoot(work string) (string, error) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(work, entries[0].Name()), nil
	}
	return work, nil
}
