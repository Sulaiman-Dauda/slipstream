package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectCacheCopiesMatch(t *testing.T) {
	b, err := os.ReadFile("../../connector/object-cache-apcu.php")
	if err != nil {
		t.Fatalf("canonical drop-in missing: %v", err)
	}
	if strings.TrimSpace(string(b)) != strings.TrimSpace(APCuDropin) {
		t.Error("connector/object-cache-apcu.php is out of sync with agent.APCuDropin")
	}
}

func TestPrecompressTree(t *testing.T) {
	dir := t.TempDir()
	// A compressible CSS file above the size floor.
	css := filepath.Join(dir, "style.css")
	os.WriteFile(css, []byte(strings.Repeat("body{color:red;margin:0;padding:0}", 200)), 0o644)
	// A tiny file (below floor — should be skipped).
	os.WriteFile(filepath.Join(dir, "tiny.css"), []byte("a{}"), 0o644)
	// An already-compressed type (should be skipped).
	os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte(strings.Repeat("x", 5000)), 0o644)

	n := precompressTree(dir)
	if n != 1 {
		t.Fatalf("expected 1 file compressed, got %d", n)
	}
	if _, err := os.Stat(css + ".gz"); err != nil {
		t.Error("style.css.gz not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "tiny.css.gz")); err == nil {
		t.Error("tiny file should not be compressed")
	}
	if _, err := os.Stat(filepath.Join(dir, "photo.jpg.gz")); err == nil {
		t.Error("jpg should not be compressed")
	}
	// The .gz must be smaller than the original.
	orig, _ := os.Stat(css)
	comp, _ := os.Stat(css + ".gz")
	if comp.Size() >= orig.Size() {
		t.Error(".gz is not smaller than original")
	}
}

func TestPathOf(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a/b/": "/a/b/",
		"https://example.com":      "/",
		"/already/a/path":          "/already/a/path",
		"http://example.com/x?y=1": "/x?y=1",
	}
	for in, want := range cases {
		if got := pathOf(in, "example.com"); got != want {
			t.Errorf("pathOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestObjectCacheDropinBehaviour runs the drop-in's own suite under both settings of
// apc.enable_cli. The zero case is the one every wp-cli process runs in, and it is where
// the drop-in used to install itself over a segment it could not reach: set, add, delete,
// incr and decr all returned false, and a flush threw. Skips when php is not installed.
func TestObjectCacheDropinBehaviour(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not installed")
	}
	for _, cli := range []string{"0", "1"} {
		t.Run("apc.enable_cli="+cli, func(t *testing.T) {
			cmd := exec.Command(php, "-d", "apc.enable_cli="+cli, "../../connector/object-cache-apcu.test.php")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("drop-in suite failed:\n%s", out)
			}
		})
	}
}
