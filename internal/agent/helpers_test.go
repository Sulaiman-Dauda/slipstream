package agent

import (
	"net/url"

	"github.com/slipstream-panel/slipstream/internal/velocity"
)

// cachePathForTest resolves the on-disk cache path for a URL the same way
// the purge engine does.
func cachePathForTest(cacheDir, raw string) string {
	u, _ := url.Parse(raw)
	return velocity.CacheFilePath(cacheDir, velocity.CacheKey("https", "GET", u.Host, u.RequestURI()))
}
