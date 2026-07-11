// Package velocity is Slipstream's performance intelligence: cache policy
// selection per site profile, and precise cache invalidation.
//
// The policies encode the product's core speed claims:
//   - full-page caching of anonymous traffic at the web-server layer
//   - request coalescing (one regeneration per URL under load)
//   - stale-while-revalidate and stale-if-error
//   - strict bypass rules so logged-in users, carts and checkouts are
//     never served cached pages
package velocity

import "github.com/slipstream-panel/slipstream/internal/state"

// CachePolicy is the concrete cache behaviour rendered into Nginx config.
type CachePolicy struct {
	Enabled bool
	// TTLSec is how long a cached page is fresh.
	TTLSec int
	// StaleSec is how long a stale page may be served while revalidating
	// or when the origin errors.
	StaleSec int
	// LockTimeoutSec bounds request coalescing waits.
	LockTimeoutSec int
	// BypassCookies: presence of any of these cookie names skips the cache.
	BypassCookies []string
	// BypassURIPrefixes: requests under these paths skip the cache.
	BypassURIPrefixes []string
	// CacheableQueryArgs: when false, any query string bypasses the cache
	// (safe default); when true, common tracking args are stripped from the
	// cache key instead.
	IgnoreTrackingArgs bool
	// ZoneSizeMB is the shared-memory key zone size.
	ZoneSizeMB int
	// MaxSizeMB caps on-disk cache size per site.
	MaxSizeMB int
}

var wordpressBypassCookies = []string{
	"wordpress_logged_in_",
	"wp-postpass_",
	"comment_author_",
	"woocommerce_cart_hash",
	"woocommerce_items_in_cart",
	"wp_woocommerce_session_",
}

var wordpressBypassURIs = []string{
	"/wp-admin",
	"/wp-login.php",
	"/wp-cron.php",
	"/xmlrpc.php",
	"/wp-json/",
	"/feed",
}

var commerceExtraURIs = []string{
	"/cart",
	"/checkout",
	"/my-account",
	"/addons",
	"/wc-api",
	"/?wc-ajax=",
}

// PolicyFor returns the cache policy for a site, derived from its profile
// and type. TTL overrides from site config are applied by the caller.
func PolicyFor(site state.Site) CachePolicy {
	// Static sites are served straight from disk and proxy caching ships in
	// a later release; the page cache applies to PHP-backed sites.
	if !site.Config.CacheEnabled || site.Type == state.SiteStatic || site.Type == state.SiteProxy {
		return CachePolicy{Enabled: false}
	}

	p := CachePolicy{
		Enabled:        true,
		LockTimeoutSec: 5,
		ZoneSizeMB:     32,
		MaxSizeMB:      1024,
	}

	switch site.Profile {
	case state.ProfileCommerce:
		p.TTLSec = 300
		p.StaleSec = 3600
		p.IgnoreTrackingArgs = false
	case state.ProfileMaximum:
		p.TTLSec = 86400
		p.StaleSec = 86400 * 3
		p.IgnoreTrackingArgs = true
		p.MaxSizeMB = 4096
	default: // Balanced
		p.TTLSec = 600
		p.StaleSec = 7200
		p.IgnoreTrackingArgs = true
	}

	if site.Config.CacheTTLSec > 0 {
		p.TTLSec = site.Config.CacheTTLSec
	}

	switch site.Type {
	case state.SiteWordPress, state.SiteWooCommerce, state.SiteLaravel, state.SitePHP:
		p.BypassCookies = append(p.BypassCookies, wordpressBypassCookies...)
		p.BypassURIPrefixes = append(p.BypassURIPrefixes, wordpressBypassURIs...)
	}
	if site.Type == state.SiteWooCommerce || site.Profile == state.ProfileCommerce {
		p.BypassURIPrefixes = append(p.BypassURIPrefixes, commerceExtraURIs...)
	}
	return p
}
