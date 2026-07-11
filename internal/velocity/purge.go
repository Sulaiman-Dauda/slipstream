package velocity

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Nginx cache entries are files named by the MD5 of the cache key, laid out
// under levels=1:2 directories. Deleting the file invalidates the entry —
// no third-party purge module required, and the agent (root) can purge any
// exact URL in microseconds.

// CacheKey builds the cache key matching the rendered fastcgi_cache_key
// "$scheme$request_method$host$request_uri".
func CacheKey(scheme, method, host, requestURI string) string {
	return scheme + method + host + requestURI
}

// CacheFilePath returns the on-disk path of a cache entry for the given key
// inside cacheDir, assuming levels=1:2 (Slipstream's rendered layout).
func CacheFilePath(cacheDir, key string) string {
	sum := md5.Sum([]byte(key))
	h := hex.EncodeToString(sum[:])
	l1 := h[len(h)-1:]
	l2 := h[len(h)-3 : len(h)-1]
	return filepath.Join(cacheDir, l1, l2, h)
}

// PurgeURLs removes the cache entries for the given absolute URLs (both GET
// variants, http and https hosts are derived from the URL itself). It
// returns how many entries existed and were removed.
func PurgeURLs(cacheDir string, urls []string) (int, error) {
	removed := 0
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return removed, fmt.Errorf("invalid purge url %q", raw)
		}
		requestURI := u.RequestURI()
		// Purge both schemes: the cache key includes $scheme and a site can
		// be warmed on either during redirects.
		for _, scheme := range []string{"https", "http"} {
			p := CacheFilePath(cacheDir, CacheKey(scheme, "GET", u.Host, requestURI))
			if err := os.Remove(p); err == nil {
				removed++
			} else if !os.IsNotExist(err) {
				return removed, fmt.Errorf("purge %s: %w", raw, err)
			}
		}
	}
	return removed, nil
}

// PurgeAll empties a site's entire cache directory. Used for full
// invalidation events (theme change, plugin update, purge-all button).
func PurgeAll(cacheDir string) (int, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		p := filepath.Join(cacheDir, e.Name())
		// Count files for reporting before removing the level directory.
		filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() {
				removed++
			}
			return nil
		})
		if err := os.RemoveAll(p); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// SanitizeCacheDirName converts a domain into a safe cache directory name.
func SanitizeCacheDirName(domain string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, domain)
}
