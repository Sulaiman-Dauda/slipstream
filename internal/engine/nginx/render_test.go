package nginx

import (
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/engine"
	"github.com/slipstream-panel/slipstream/internal/state"
	"github.com/slipstream-panel/slipstream/internal/velocity"
)

func wooSite() state.Site {
	return state.Site{
		ID:         7,
		Domain:     "shop.example.com",
		Aliases:    []string{"www.shop.example.com"},
		Type:       state.SiteWooCommerce,
		Profile:    state.ProfileCommerce,
		Engine:     state.EngineNginx,
		PHPVersion: "8.4",
		SystemUser: "slip-site-7",
		RootPath:   "/srv/sites/shop.example.com",
		Config:     state.SiteConfig{CacheEnabled: true},
	}
}

func renderWoo(t *testing.T) map[string]string {
	t.Helper()
	site := wooSite()
	files, err := Renderer{}.SiteFiles(engine.Input{
		Site:          site,
		Policy:        velocity.PolicyFor(site),
		DocRoot:       "/srv/sites/shop.example.com/current",
		PHPSocket:     "/run/slipstream/php/slip-site-7.sock",
		CacheDir:      "/var/cache/slipstream/shop.example.com",
		LogDir:        "/var/log/slipstream/shop.example.com",
		CertAvailable: true,
		CertFullchain: "/etc/letsencrypt/live/shop.example.com/fullchain.pem",
		CertKey:       "/etc/letsencrypt/live/shop.example.com/privkey.pem",
		ACMEWebroot:   "/var/lib/slipstream/acme",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return files
}

func TestWooCommerceVhost(t *testing.T) {
	files := renderWoo(t)

	vhost, ok := files["/etc/nginx/sites-enabled/shop.example.com.conf"]
	if !ok {
		t.Fatalf("missing vhost, got files: %v", keys(files))
	}
	zone, ok := files["/etc/nginx/conf.d/slipstream-cache-7.conf"]
	if !ok {
		t.Fatalf("missing cache zone, got files: %v", keys(files))
	}

	// Velocity Engine invariants — every one of these is a product claim.
	for _, want := range []string{
		"fastcgi_cache slip_7;",
		`fastcgi_cache_key "$scheme$request_method$host$request_uri$slip_enc";`,
		"fastcgi_cache_lock on;",                 // request coalescing
		"fastcgi_cache_use_stale updating error", // SWR + stale-if-error
		"fastcgi_cache_background_update on;",
		"add_header X-Slipstream-Cache $upstream_cache_status always;",
		"fastcgi_pass unix:/run/slipstream/php/slip-site-7.sock;",
		"server_name shop.example.com www.shop.example.com;",
		"return 301 https://$host$request_uri;",
		"/.well-known/acme-challenge/",
		"ssl_certificate /etc/letsencrypt/live/shop.example.com/fullchain.pem;",
	} {
		if !strings.Contains(vhost, want) {
			t.Errorf("vhost missing %q", want)
		}
	}

	// Commerce bypass rules: carts and logged-in users never see the cache.
	for _, want := range []string{
		"woocommerce_items_in_cart",
		"wordpress_logged_in_",
		"/cart",
		"/checkout",
		"/my-account",
		"/wp-admin",
	} {
		if !strings.Contains(vhost, want) {
			t.Errorf("vhost missing commerce bypass %q", want)
		}
	}

	if !strings.Contains(zone, "keys_zone=slip_7:32m") {
		t.Errorf("zone missing keys_zone: %s", zone)
	}
	if !strings.Contains(zone, "fastcgi_cache_path /var/cache/slipstream/shop.example.com levels=1:2") {
		t.Errorf("zone missing cache path: %s", zone)
	}
}

func TestCommerceProfileTTL(t *testing.T) {
	site := wooSite()
	p := velocity.PolicyFor(site)
	if p.TTLSec != 300 {
		t.Errorf("commerce TTL = %d, want 300", p.TTLSec)
	}
	site.Profile = state.ProfileMaximum
	if p := velocity.PolicyFor(site); p.TTLSec != 86400 {
		t.Errorf("maximum TTL = %d, want 86400", p.TTLSec)
	}
	site.Config.CacheTTLSec = 42
	if p := velocity.PolicyFor(site); p.TTLSec != 42 {
		t.Errorf("override TTL = %d, want 42", p.TTLSec)
	}
}

func TestStaticSiteHasNoPHPOrCache(t *testing.T) {
	site := state.Site{
		ID: 3, Domain: "docs.example.com", Type: state.SiteStatic,
		Profile: state.ProfileBalanced, SystemUser: "slip-site-3",
		RootPath: "/srv/sites/docs.example.com",
		Config:   state.SiteConfig{CacheEnabled: true},
	}
	files, err := Renderer{}.SiteFiles(engine.Input{
		Site: site, Policy: velocity.PolicyFor(site),
		DocRoot:      "/srv/sites/docs.example.com/current",
		LogDir:       "/var/log/slipstream/docs.example.com",
		FallbackCert: "/etc/slipstream/certs/fallback.pem", FallbackKey: "/etc/slipstream/certs/fallback.key",
		ACMEWebroot: "/var/lib/slipstream/acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	vhost := files["/etc/nginx/sites-enabled/docs.example.com.conf"]
	if strings.Contains(vhost, "fastcgi") {
		t.Error("static vhost must not contain fastcgi directives")
	}
	if _, ok := files["/etc/nginx/conf.d/slipstream-cache-3.conf"]; ok {
		t.Error("static site must not get a cache zone")
	}
	if !strings.Contains(vhost, "ssl_certificate /etc/slipstream/certs/fallback.pem;") {
		t.Error("expected fallback certificate before ACME issuance")
	}
}

func TestProxySite(t *testing.T) {
	site := state.Site{
		ID: 9, Domain: "app.example.com", Type: state.SiteProxy,
		Profile: state.ProfileBalanced, SystemUser: "slip-site-9",
		RootPath: "/srv/sites/app.example.com",
		Config:   state.SiteConfig{ProxyUpstream: "http://127.0.0.1:3000"},
	}
	files, err := Renderer{}.SiteFiles(engine.Input{
		Site: site, Policy: velocity.PolicyFor(site),
		LogDir:        "/var/log/slipstream/app.example.com",
		CertAvailable: true, CertFullchain: "/c.pem", CertKey: "/k.pem",
		ACMEWebroot: "/var/lib/slipstream/acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	vhost := files["/etc/nginx/sites-enabled/app.example.com.conf"]
	if !strings.Contains(vhost, "proxy_pass http://127.0.0.1:3000;") {
		t.Error("proxy vhost missing proxy_pass")
	}
	if !strings.Contains(vhost, `proxy_set_header Connection "upgrade";`) {
		t.Error("proxy vhost missing websocket upgrade")
	}
}

func TestDomainValidationBlocksInjection(t *testing.T) {
	bad := []string{
		"evil.com; }", "a b.com", "UPPER..com", "-lead.com", "com",
		"$(rm -rf /).com", "exa\nmple.com",
	}
	for _, d := range bad {
		if err := ValidateDomain(d); err == nil {
			t.Errorf("expected rejection of %q", d)
		}
	}
	good := []string{"example.com", "www.example.com", "a-b.co.uk", "xn--nxasmq6b.com"}
	for _, d := range good {
		if err := ValidateDomain(d); err != nil {
			t.Errorf("expected %q to validate: %v", d, err)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// nginx inherits add_header from the enclosing level only when the current
// level declares none. The php and static locations each declare one of their
// own, so a single server-level block is discarded exactly where every real
// request is served. This shipped: live boxes returned no security headers at
// all on any WordPress page.
func secSite() state.Site {
	return state.Site{
		ID: 5, Domain: "sec.example.com", SystemUser: "slip-site-5",
		PHPVersion: "8.4", Type: state.SiteWordPress,
		RootPath: "/srv/sites/sec.example.com",
		Config:   state.SiteConfig{CacheEnabled: true},
	}
}

func TestSecurityHeadersPresentInEveryLocationThatSetsHeaders(t *testing.T) {
	files, err := Renderer{SitesDir: t.TempDir(), ConfDir: t.TempDir()}.SiteFiles(engine.Input{
		Site:      secSite(),
		Policy:    velocity.PolicyFor(secSite()),
		DocRoot:   "/srv/sites/sec.example.com/current",
		PHPSocket: "/run/slipstream/php/slip-site-5.sock",
		LogDir:    "/var/log/slipstream/sec.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	var vhost string
	for p, c := range files {
		if strings.HasSuffix(p, "sec.example.com.conf") {
			vhost = c
		}
	}
	if vhost == "" {
		t.Fatal("vhost not rendered")
	}
	// One occurrence per level that declares add_header: server, static, php.
	for _, h := range []string{
		"add_header X-Content-Type-Options nosniff always;",
		"add_header X-Frame-Options SAMEORIGIN always;",
		"add_header Referrer-Policy strict-origin-when-cross-origin always;",
	} {
		if n := strings.Count(vhost, h); n < 3 {
			t.Errorf("security header %q appears %d times, want >=3 (server + static + php locations)", h, n)
		}
	}
	// And it must land inside the php location, after X-Slipstream-Cache.
	i := strings.Index(vhost, "add_header X-Slipstream-Cache")
	if i < 0 {
		t.Fatal("expected the cache-status header in the php location")
	}
	if !strings.Contains(vhost[i:], "X-Content-Type-Options") {
		t.Error("php location sets add_header but does not re-declare the security headers")
	}
}
