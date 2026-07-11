package agent

import (
	"context"
	"errors"
	"os"
	"testing"
)

// fakeFailRunner fails every command, exercising fallback paths.
type fakeFailRunner struct{}

func (fakeFailRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("unavailable")
}
func (fakeFailRunner) RunStdin(context.Context, string, string, ...string) (string, error) {
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
