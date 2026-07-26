package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The file manager runs as root on directories a tenant owns. A path check
// followed by a separate open is racy: the tenant can swap a component for a
// symlink in between and redirect root's read or write outside the jail.
func TestOpenBeneathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("root-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Symlinks planted inside the jail pointing out of it — exactly what a
	// compromised site would create.
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escapedir")); err != nil {
		t.Fatal(err)
	}

	// Legitimate access still works.
	f, err := openBeneath(root, "ok.txt", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("legitimate open failed: %v", err)
	}
	f.Close()

	for _, rel := range []string{
		"escape",           // symlink to a file outside the root
		"escapedir/secret", // symlink to a directory outside the root
		"../" + filepath.Base(outside) + "/secret", // traversal above the root
	} {
		f, err := openBeneath(root, rel, unix.O_RDONLY, 0)
		if err == nil {
			f.Close()
			t.Errorf("openBeneath(%q) succeeded — it must not escape the site root", rel)
		}
	}
}

// The panel's own managed symlinks are ABSOLUTE (a release "current" pointer,
// wp-config and uploads pointing into shared/) and must keep working. An
// over-strict jail that refuses them breaks the file manager for every real
// site, which is worse than the race it closes — RESOLVE_BENEATH does exactly
// that, which is why this uses RESOLVE_NO_SYMLINKS on a pre-resolved path.
func TestOpenBeneathFollowsManagedLayout(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "releases", "initial")
	shared := filepath.Join(root, "shared")
	if err := os.MkdirAll(release, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "wp-config.php"), []byte("<?php // config"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Absolute symlinks, exactly as the provisioner creates them.
	if err := os.Symlink(release, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "wp-config.php"), filepath.Join(release, "wp-config.php")); err != nil {
		t.Fatal(err)
	}

	f, err := openBeneath(root, "current/wp-config.php", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("reading through the managed release symlinks must work: %v", err)
	}
	f.Close()

	if err := writeBeneath(root, "current/new.txt", "root", "hello", 0o640); err != nil {
		t.Fatalf("writing inside the release must work: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(release, "new.txt")); strings.TrimSpace(string(b)) != "hello" {
		t.Errorf("file did not land in the release directory, got %q", b)
	}
}

// writeBeneath must not create a file through a symlink that leaves the root.
func TestWriteBeneathStaysInside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "pwned")
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := writeBeneath(root, "link", "root", "attacker content", 0o640); err == nil {
		if _, statErr := os.Stat(target); statErr == nil {
			t.Fatal("write followed a symlink out of the site root")
		}
	}
	// Writing into an existing directory works. (Creating missing parents is
	// not the write contract — the file manager has a separate mkdir — and was
	// never supported here.)
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeBeneath(root, "notes/new.txt", "root", "hello", 0o640); err != nil {
		t.Fatalf("legitimate nested write failed: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "notes", "new.txt"))
	if strings.TrimSpace(string(b)) != "hello" {
		t.Errorf("expected the file inside the root, got %q", b)
	}
}
