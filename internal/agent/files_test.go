package agent

import (
	"context"
	"errors"
	"testing"
)

// fakeFailRunner fails every command, exercising fallback paths.
type fakeFailRunner struct{}

func (fakeFailRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("unavailable")
}

func TestResolveInSiteJail(t *testing.T) {
	root := "/srv/sites/example.com"
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
	// Traversal attempts are neutralized (collapsed at root), never escape:
	// every result must stay within root.
	traversal := []string{"../../../etc/passwd", "../other-site", "/../../root", "../../", "wp-content/../../../.."}
	for _, e := range traversal {
		got, err := resolveInSite(root, e)
		if err != nil {
			continue // rejection is also acceptable
		}
		if got != root && !hasPrefix(got, root+"/") {
			t.Errorf("resolveInSite(%q) = %q escaped the jail", e, got)
		}
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
