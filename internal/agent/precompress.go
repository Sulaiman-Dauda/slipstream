package agent

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Precompressing static assets at deploy time lets nginx serve a ready-made
// .gz via gzip_static instead of recompressing on every request (the
// benchmark showed per-request gzip is real CPU). We only precompress text
// asset types that benefit; already-compressed formats (images, video,
// fonts) are skipped.
var compressibleExt = map[string]bool{
	".css": true, ".js": true, ".mjs": true, ".svg": true,
	".json": true, ".xml": true, ".html": true, ".txt": true, ".map": true, ".ico": false,
}

// precompressTree writes a .gz next to every compressible file under root
// whose gzip is meaningfully smaller. Returns the count compressed.
func precompressTree(root string) int {
	count := 0
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !compressibleExt[ext] {
			return nil
		}
		// Skip tiny files (compression overhead not worth it) and huge ones.
		if info.Size() < 1024 || info.Size() > 10<<20 {
			return nil
		}
		if _, err := os.Stat(path + ".gz"); err == nil {
			return nil // already compressed
		}
		if gzipFile(path) {
			count++
		}
		return nil
	})
	return count
}

func gzipFile(path string) bool {
	in, err := os.Open(path)
	if err != nil {
		return false
	}
	defer in.Close()
	tmp := path + ".gz.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return false
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return false
	}
	zw.Close()
	out.Close()
	// Only keep the .gz if it is actually smaller.
	orig, _ := os.Stat(path)
	comp, _ := os.Stat(tmp)
	if comp == nil || orig == nil || comp.Size() >= orig.Size() {
		os.Remove(tmp)
		return false
	}
	if os.Rename(tmp, path+".gz") != nil {
		os.Remove(tmp)
		return false
	}
	return true
}
