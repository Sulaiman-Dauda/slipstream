package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

var releaseIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$|^initial$`)

// DeployRelease copies a prepared source tree into an immutable release
// directory and links shared persistent paths into it. Promotion is a
// separate, atomic step.
func (a *Agent) DeployRelease(p rpc.DeployParams) (rpc.DeployResult, error) {
	ctx := context.Background()
	site := p.Site
	if err := validateSite(site); err != nil {
		return rpc.DeployResult{}, err
	}
	if !releaseIDRe.MatchString(p.ReleaseID) {
		return rpc.DeployResult{}, fmt.Errorf("invalid release id %q", p.ReleaseID)
	}
	// Resolve symlinks: deploying from a site's `current` link must copy
	// the release content, not the link itself.
	sourceDir, err := filepath.EvalSymlinks(p.SourceDir)
	if err != nil {
		return rpc.DeployResult{}, fmt.Errorf("source dir %q unavailable: %v", p.SourceDir, err)
	}
	if fi, err := os.Stat(sourceDir); err != nil || !fi.IsDir() {
		return rpc.DeployResult{}, fmt.Errorf("source dir %q unavailable: %v", sourceDir, err)
	}

	releaseDir := filepath.Join(site.RootPath, "releases", p.ReleaseID)
	if _, err := os.Stat(releaseDir); err == nil {
		return rpc.DeployResult{}, fmt.Errorf("release %s already exists", p.ReleaseID)
	}
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0o750); err != nil {
		return rpc.DeployResult{}, err
	}

	// Any failure after the directory is created removes the partial release
	// so a retry with the same ID is not blocked by "release already exists".
	fail := func(err error) (rpc.DeployResult, error) {
		os.RemoveAll(releaseDir)
		return rpc.DeployResult{}, err
	}

	// cp -a preserves permissions and is dramatically faster than a Go file
	// walk for large trees.
	if _, err := a.Runner.Run(ctx, "cp", "-a", sourceDir, releaseDir); err != nil {
		return fail(fmt.Errorf("copy release: %w", err))
	}

	// Shared persistent data lives outside releases.
	uploads := filepath.Join(releaseDir, "wp-content", "uploads")
	if _, err := os.Stat(filepath.Dir(uploads)); err == nil {
		os.RemoveAll(uploads)
		if err := forceSymlink(filepath.Join(site.RootPath, "shared", "uploads"), uploads); err != nil {
			return fail(err)
		}
	}
	// Configuration always comes from THIS site's shared/, never from the
	// deployed tree — a release cloned from staging must not carry staging
	// credentials into production.
	sharedCfg := filepath.Join(site.RootPath, "shared", "wp-config.php")
	if _, err := os.Stat(sharedCfg); err == nil {
		os.Remove(filepath.Join(releaseDir, "wp-config.php"))
		if err := forceSymlink(sharedCfg, filepath.Join(releaseDir, "wp-config.php")); err != nil {
			return fail(err)
		}
	}

	if _, err := a.Runner.Run(ctx, "chown", "-R", site.SystemUser+":"+site.SystemUser, releaseDir); err != nil {
		return fail(err)
	}

	// Precompress static assets in the new release.
	precompressTree(releaseDir)

	sum, err := hashTree(releaseDir)
	if err != nil {
		return fail(err)
	}
	return rpc.DeployResult{ReleaseID: p.ReleaseID, Path: releaseDir, Checksum: sum}, nil
}

// PromoteRelease atomically points current at a release and reloads PHP so
// OPcache serves the new code.
func (a *Agent) PromoteRelease(p rpc.ReleaseParams) (map[string]string, error) {
	ctx := context.Background()
	if err := validateSite(p.Site); err != nil {
		return nil, err
	}
	if !releaseIDRe.MatchString(p.ReleaseID) {
		return nil, fmt.Errorf("invalid release id %q", p.ReleaseID)
	}
	releaseDir := filepath.Join(p.Site.RootPath, "releases", p.ReleaseID)
	if fi, err := os.Stat(releaseDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("release %s not found", p.ReleaseID)
	}
	if err := forceSymlink(releaseDir, filepath.Join(p.Site.RootPath, "current")); err != nil {
		return nil, err
	}
	if p.Site.PHPVersion != "" {
		if err := a.reloadPHPFPM(ctx, p.Site.PHPVersion); err != nil {
			return nil, err
		}
	}
	return map[string]string{"current": p.ReleaseID}, nil
}

// RollbackRelease repoints current at the previous release (or an explicit
// one) — the instant-rollback promise.
func (a *Agent) RollbackRelease(p rpc.ReleaseParams) (map[string]string, error) {
	target := p.ReleaseID
	if target == "" {
		prev, err := previousRelease(p.Site.RootPath)
		if err != nil {
			return nil, err
		}
		target = prev
	}
	return a.PromoteRelease(rpc.ReleaseParams{Site: p.Site, ReleaseID: target})
}

// previousRelease finds the newest release that is not the current one.
func previousRelease(rootPath string) (string, error) {
	current, err := os.Readlink(filepath.Join(rootPath, "current"))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, "releases"))
	if err != nil {
		return "", err
	}
	var timestamped, other []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == filepath.Base(current) {
			continue
		}
		if releaseTimestampRe.MatchString(e.Name()) {
			timestamped = append(timestamped, e.Name())
		} else {
			other = append(other, e.Name()) // e.g. "initial"
		}
	}
	// Prefer the newest real timestamped release; the "initial" scaffold is
	// only a fallback when no deploys have happened. (Lexically "initial"
	// sorts after every timestamp, so a naive max would wrongly pick it.)
	if len(timestamped) > 0 {
		sort.Strings(timestamped)
		return timestamped[len(timestamped)-1], nil
	}
	if len(other) > 0 {
		sort.Strings(other)
		return other[len(other)-1], nil
	}
	return "", fmt.Errorf("no previous release to roll back to")
}

var releaseTimestampRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)

// hashTree produces a deterministic checksum of a directory tree's file
// paths and contents.
func hashTree(root string) (string, error) {
	h := sha256.New()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		// Length-prefix the path so {"ab","c"} and {"a","bc"} can't collide.
		fmt.Fprintf(h, "%d:%s\x00", len(rel), rel)
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
