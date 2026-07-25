package phpfpm

import (
	"strings"
	"testing"

	"github.com/slipstream-panel/slipstream/internal/state"
)

func TestSizeWorkers(t *testing.T) {
	cases := []struct {
		memMB, requested, wantMax int
	}{
		{1024, 0, 12},  // 1 GB / 80 MB
		{4096, 0, 51},  // 4 GB
		{128, 0, 2},    // floor
		{65536, 0, 64}, // ceiling
		{1024, 6, 6},   // explicit request wins
	}
	for _, c := range cases {
		w := SizeWorkers(c.memMB, c.requested)
		if w.Max != c.wantMax {
			t.Errorf("SizeWorkers(%d, %d).Max = %d, want %d", c.memMB, c.requested, w.Max, c.wantMax)
		}
		if w.MinSpare < 1 || w.StartServe < 1 || w.MaxSpare < w.StartServe {
			t.Errorf("invalid spare config: %+v", w)
		}
	}
}

func TestSizeOPcache(t *testing.T) {
	small := SizeOPcache(512, state.SiteWordPress)
	if small.MemoryMB != 128 {
		t.Errorf("small opcache = %d, want 128", small.MemoryMB)
	}
	big := SizeOPcache(8192, state.SiteWordPress)
	if big.MemoryMB != 512 || big.MaxFiles != 100000 {
		t.Errorf("big opcache = %+v", big)
	}
	woo := SizeOPcache(512, state.SiteWooCommerce)
	if woo.MaxFiles < 50000 {
		t.Errorf("woocommerce needs more cached files, got %d", woo.MaxFiles)
	}
}

func TestRenderPool(t *testing.T) {
	site := state.Site{
		ID: 7, Domain: "shop.example.com", Type: state.SiteWooCommerce,
		PHPVersion: "8.4", SystemUser: "slip-site-7",
		RootPath: "/srv/sites/shop.example.com",
	}
	path, content, err := RenderPool(site, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/etc/php/8.4/fpm/pool.d/slip-site-7.conf" {
		t.Errorf("unexpected path %s", path)
	}
	for _, want := range []string{
		"[slip-site-7]",
		"user = slip-site-7",
		"listen = /run/slipstream/php/slip-site-7.sock",
		"pm = dynamic",
		"php_admin_value[open_basedir] = /srv/sites/shop.example.com:/usr/share/php",
		"php_admin_flag[opcache.enable] = on",
		"php_admin_value[opcache.memory_consumption] = 256",
		"php_admin_value[memory_limit] = 512M", // WooCommerce gets headroom
		"php_admin_value[disable_functions] = exec,passthru",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("pool missing %q", want)
		}
	}
	// /tmp must not be on open_basedir: it is a cross-tenant channel and all
	// temp uses are redirected to the site's own tmp.
	if strings.Contains(content, ":/tmp:") {
		t.Errorf("open_basedir must not include /tmp (cross-tenant channel)")
	}

	if _, _, err := RenderPool(state.Site{Domain: "x.com"}, 0); err == nil {
		t.Error("expected error for missing user/php version")
	}
}
