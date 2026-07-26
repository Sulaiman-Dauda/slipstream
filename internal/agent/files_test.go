package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// fakeFailRunner fails every command, exercising fallback paths.
type fakeFailRunner struct{}

func (fakeFailRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("unavailable")
}
func (fakeFailRunner) RunStdin(context.Context, string, string, ...string) (string, error) {
	return "", errors.New("unavailable")
}
func (fakeFailRunner) RunCombined(context.Context, string, ...string) (string, error) {
	return "", errors.New("unavailable")
}

func TestResolveInSiteJail(t *testing.T) {
	// resolveInSite resolves symlinks, so use a real on-disk root.
	root := t.TempDir()
	os.MkdirAll(root+"/wp-content/uploads", 0o755)
	os.MkdirAll(root+"/b", 0o755)

	ok := []struct{ rel, want string }{
		{"", root},
		{"wp-content", root + "/wp-content"},
		{"/wp-content/uploads", root + "/wp-content/uploads"},
		{"a/../b", root + "/b"},
	}
	for _, c := range ok {
		got, err := resolveInSite(root, c.rel)
		if err != nil || got != c.want {
			t.Errorf("resolveInSite(%q) = %q, %v; want %q", c.rel, got, err, c.want)
		}
	}
	// Traversal attempts are neutralized (collapsed at root), never escape.
	traversal := []string{"../../../etc/passwd", "../other-site", "/../../root", "../../"}
	for _, e := range traversal {
		got, err := resolveInSite(root, e)
		if err != nil {
			continue
		}
		if got != root && !hasPrefix(got, root+"/") {
			t.Errorf("resolveInSite(%q) = %q escaped the jail", e, got)
		}
	}

	// A symlink pointing outside the jail must be rejected.
	os.Symlink("/etc", root+"/evil")
	if _, err := resolveInSite(root, "evil/passwd"); err == nil {
		t.Error("symlink escape was not rejected")
	}
}

func TestManagedFileWorkflow(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ManageFile(rpc.ManageFileParams{Site: site, Operation: "mkdir", RelPath: "shared/assets"}); err != nil {
		t.Fatal(err)
	}
	data := []byte{0, 1, 2, 3, 255}
	if _, err := a.TransferFile(rpc.TransferFileParams{Site: site, RelPath: "shared/assets/blob.bin", Data: data, Upload: true}); err != nil {
		t.Fatal(err)
	}
	got, err := a.TransferFile(rpc.TransferFileParams{Site: site, RelPath: "shared/assets/blob.bin"})
	if err != nil || string(got.Data) != string(data) {
		t.Fatalf("download = %v, %v", got.Data, err)
	}
	if _, err := a.ManageFile(rpc.ManageFileParams{Site: site, Operation: "rename", RelPath: "shared/assets/blob.bin", DestPath: "shared/assets/renamed.bin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site.RootPath, "shared/assets/renamed.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ManageFile(rpc.ManageFileParams{Site: site, Operation: "delete", RelPath: "shared/assets"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(site.RootPath, "shared/assets")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory still exists: %v", err)
	}
	if _, err := a.TransferFile(rpc.TransferFileParams{Site: site, RelPath: "too-big", Data: []byte(strings.Repeat("x", maxTransferFile+1)), Upload: true}); err == nil {
		t.Fatal("oversized upload accepted")
	}
}

// A RelPath that cleans down to the site root (not just the literal empty
// string) must never reach a destructive operation — "/" and "." both
// resolve to RootPath via resolveInSite, and a bare emptiness check on
// RelPath does not catch them.
func TestManageFileRejectsRootPathVariants(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"delete", "mkdir"} {
		for _, rel := range []string{"/", ".", "//", "/."} {
			if _, err := a.ManageFile(rpc.ManageFileParams{Site: site, Operation: op, RelPath: rel}); err == nil {
				t.Fatalf("%s on RelPath %q against site root was not rejected", op, rel)
			}
		}
	}
	if _, err := os.Stat(site.RootPath); err != nil {
		t.Fatalf("site root should still exist: %v", err)
	}
}

// ListFiles must report a symlink-to-a-directory (the site's "current"
// release pointer, in production) as a directory, not a plain file — the
// raw Lstat mode from ReadDir reflects the symlink itself, not its target.
func TestListFilesFollowsSymlinkForIsDir(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}
	res, err := a.ListFiles(rpc.ListFilesParams{Site: site, RelPath: ""})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range res.Entries {
		if e.Name == "current" {
			found = true
			if !e.IsDir {
				t.Error("current (symlink to releases/initial) reported as a file, not a directory")
			}
		}
	}
	if !found {
		t.Fatal("current entry not listed")
	}
}

func TestSSHKeyWorkflow(t *testing.T) {
	a, _ := testAgent(t)
	site := staticSite(a)
	if _, err := a.CreateSite(rpc.CreateSiteParams{Site: site}); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 48)))
	added, err := a.SSHKeys(rpc.SSHKeyParams{Site: site, Action: "add", PublicKey: "ssh-ed25519 " + encoded + " dev@laptop"})
	if err != nil || len(added.Keys) != 1 || added.Keys[0].Label != "dev@laptop" {
		t.Fatalf("add key = %+v, %v", added, err)
	}
	if _, err := a.SSHKeys(rpc.SSHKeyParams{Site: site, Action: "add", PublicKey: "ssh-ed25519 " + encoded}); err == nil {
		t.Fatal("duplicate key accepted")
	}
	removed, err := a.SSHKeys(rpc.SSHKeyParams{Site: site, Action: "delete", Fingerprint: added.Keys[0].Fingerprint})
	if err != nil || len(removed.Keys) != 0 {
		t.Fatalf("delete key = %+v, %v", removed, err)
	}
	if _, err := a.SSHKeys(rpc.SSHKeyParams{Site: site, Action: "add", PublicKey: "ssh-dss invalid"}); err == nil {
		t.Fatal("unsupported key accepted")
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func TestPHPStringEscaping(t *testing.T) {
	if got := phpStr("O'Brien"); got != `'O\'Brien'` {
		t.Errorf("phpStr = %q", got)
	}
	if got := phpStr(`a\b`); got != `'a\\b'` {
		t.Errorf("phpStr backslash = %q", got)
	}
}

func TestPhpFPMUnitFallback(t *testing.T) {
	a := &Agent{Runner: fakeFailRunner{}}
	if u := a.phpFPMUnit(); u != "php8.4-fpm" {
		t.Errorf("fallback unit = %q", u)
	}
}
