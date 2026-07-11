package velocity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/state"
)

func TestCacheFilePathLayout(t *testing.T) {
	// levels=1:2 → …/<last-1-char>/<prev-2-chars>/<md5hex>
	key := CacheKey("https", "GET", "example.com", "/hello/")
	p := CacheFilePath("/var/cache/slipstream/example.com", key)

	dir1 := filepath.Base(filepath.Dir(filepath.Dir(p)))
	dir2 := filepath.Base(filepath.Dir(p))
	name := filepath.Base(p)
	if len(name) != 32 {
		t.Fatalf("expected md5 hex name, got %q", name)
	}
	if dir1 != name[31:] {
		t.Errorf("level1 dir %q != last char %q", dir1, name[31:])
	}
	if dir2 != name[29:31] {
		t.Errorf("level2 dir %q != chars 29..31 %q", dir2, name[29:31])
	}
}

func TestPurgeURLs(t *testing.T) {
	dir := t.TempDir()
	url := "https://example.com/2026/07/big-post/"
	p := CacheFilePath(dir, CacheKey("https", "GET", "example.com", "/2026/07/big-post/"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("cached page"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := PurgeURLs(dir, []string{url})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("cache file still exists")
	}

	// Purging a cold URL is a no-op, not an error.
	removed, err = PurgeURLs(dir, []string{"https://example.com/cold/"})
	if err != nil || removed != 0 {
		t.Fatalf("cold purge: removed=%d err=%v", removed, err)
	}

	if _, err := PurgeURLs(dir, []string{"::not a url::"}); err == nil {
		t.Fatal("expected error for invalid url")
	}
}

func TestPurgeAll(t *testing.T) {
	dir := t.TempDir()
	for _, uri := range []string{"/a/", "/b/", "/c/"} {
		p := CacheFilePath(dir, CacheKey("https", "GET", "x.com", uri))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	removed, err := PurgeAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("cache dir not empty")
	}
}

func TestPolicyBypassRules(t *testing.T) {
	site := state.Site{Type: state.SiteWordPress, Profile: state.ProfileBalanced, Config: state.SiteConfig{CacheEnabled: true}}
	p := PolicyFor(site)
	if !p.Enabled {
		t.Fatal("expected enabled policy")
	}
	found := false
	for _, c := range p.BypassCookies {
		if c == "wordpress_logged_in_" {
			found = true
		}
	}
	if !found {
		t.Error("missing logged-in cookie bypass")
	}

	site.Config.CacheEnabled = false
	if PolicyFor(site).Enabled {
		t.Error("disabled cache must yield disabled policy")
	}
}
