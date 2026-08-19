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
	files, size, skipped, err := extractMigrationArchive(archivePath, work)
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
	// An imported tree carries the old host's wp-content, so it has no
	// connector. Reinstate it before the release is built, so DeployRelease's
	// recursive chown covers it and cache invalidation survives the import.
	if p.Site.Type == state.SiteWordPress || p.Site.Type == state.SiteWooCommerce {
		if err := installConnector(source); err != nil {
			return rpc.MigrationResult{}, fmt.Errorf("reinstall cache connector: %w", err)
		}
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
	var safetyDB, sqlPath string
	if p.SQL != "" {
		if !p.Site.Config.Database.Enabled || p.Site.Config.Database.External {
			return rpc.MigrationResult{}, fmt.Errorf("site has no local managed database")
		}
		var err error
		sqlPath, err = resolveInSite(p.Site.RootPath, p.SQL)
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
			// The safety DB re-import restores different rows than FPM has
			// cached in APCu, which SIGUSR2 preserves — flush, do not reload.
			_ = a.refreshSiteState(context.Background(), p.Site)
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
	// A migration imports a foreign database and rewrites URLs across it under
	// wp-cli, whose APCu segment is separate from FPM's. Flush this site's
	// object cache so the live site serves the migrated options and rewrite
	// rules instead of whatever APCu held before.
	if p.Site.PHPVersion != "" && (p.Site.Type == state.SiteWordPress || p.Site.Type == state.SiteWooCommerce) {
		_ = a.refreshSiteState(ctx, p.Site)
	}
	cleanupRelease = false

	// The import succeeded, so the inputs have served their purpose. Leaving
	// them costs real disk on the small servers this targets (a 25 GB box
	// loses several percent per site), and one of the two is a full database
	// dump sitting inside the site tree. Removal is deliberately last: until
	// this point a failure rolls back and the operator can retry without
	// re-uploading anything.
	_ = os.Remove(archivePath)
	if sqlPath != "" {
		_ = os.Remove(sqlPath)
	}

	return rpc.MigrationResult{ReleaseID: p.ReleaseID, Files: files, Bytes: size, Skipped: skipped}, nil
}

// inRoot reports whether an already-cleaned absolute path is dest or lives
// beneath it — the containment invariant every extracted entry must satisfy.
func inRoot(p, dest string) bool {
	return p == dest || strings.HasPrefix(p, dest+string(os.PathSeparator))
}

func extractMigrationArchive(path, dest string) (int, int64, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("open migration archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files, skipped int
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return files, total, skipped, err
		}
		name := filepath.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return files, total, skipped, fmt.Errorf("archive path escapes destination")
		}
		target := filepath.Join(dest, name)
		if !inRoot(target, dest) {
			return files, total, skipped, fmt.Errorf("archive path escapes destination")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, total, skipped, err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			total += h.Size
			if files > maxMigrationFiles || total > maxMigrationBytes {
				return files, total, skipped, fmt.Errorf("migration archive exceeds safety limits")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, total, skipped, err
			}
			mode := os.FileMode(0o644)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return files, total, skipped, err
			}
			_, copyErr := io.CopyN(out, tr, h.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return files, total, skipped, copyErr
			}
			if closeErr != nil {
				return files, total, skipped, closeErr
			}
		case tar.TypeSymlink:
			// Real WordPress/Laravel trees legitimately contain symlinks
			// (object-cache.php drop-ins, vendored packages). Recreate only
			// those whose target stays inside the extraction root; anything
			// pointing outside (an absolute old-host path, or ../ past the
			// root) is skipped, not followed — this preserves the tar
			// symlink-escape protection while no longer failing the whole
			// migration over one ordinary drop-in link.
			linkFull := h.Linkname
			if !filepath.IsAbs(linkFull) {
				linkFull = filepath.Join(filepath.Dir(target), h.Linkname)
			}
			if !inRoot(filepath.Clean(linkFull), dest) {
				skipped++
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, total, skipped, err
			}
			if err := os.Symlink(h.Linkname, target); err != nil && !os.IsExist(err) {
				return files, total, skipped, err
			}
		case tar.TypeLink:
			// Hardlink to another archive member. Only allowed when the
			// source already resolves inside the root; a link to an absolute
			// system path would smuggle its contents into the release.
			srcFull := filepath.Clean(filepath.Join(dest, h.Linkname))
			if !inRoot(srcFull, dest) {
				skipped++
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, total, skipped, err
			}
			if err := os.Link(srcFull, target); err != nil {
				// Out-of-order or missing source: skip rather than abort.
				skipped++
			}
		default:
			// Devices, FIFOs and other special files have no place in a
			// web-site release; skip them without aborting the migration.
			skipped++
		}
	}
	if files == 0 {
		return 0, 0, skipped, fmt.Errorf("migration archive is empty")
	}
	return files, total, skipped, nil
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
