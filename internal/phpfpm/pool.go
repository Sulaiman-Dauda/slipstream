// Package phpfpm renders per-site PHP-FPM pools with hardware-aware worker
// counts and OPcache sizing. Every site gets its own pool, socket, Unix user
// and open_basedir jail.
package phpfpm

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// PoolDirFor returns the pool.d directory for a PHP version.
func PoolDirFor(phpVersion string) string {
	return fmt.Sprintf("/etc/php/%s/fpm/pool.d", phpVersion)
}

// SocketFor returns the FPM socket path for a site's system user.
func SocketFor(systemUser string) string {
	return filepath.Join("/run/slipstream/php", systemUser+".sock")
}

// OPcache is the calculated OPcache allocation for one pool.
type OPcache struct {
	MemoryMB          int
	InternedStringsMB int
	MaxFiles          int
}

// Workers is the calculated process-manager configuration.
type Workers struct {
	Max         int
	StartServe  int
	MinSpare    int
	MaxSpare    int
	MaxRequests int
}

// SizeOPcache calculates OPcache allocation from the memory available to
// this site (its cgroup memory-high, or a share of system RAM when
// unlimited).
func SizeOPcache(siteMemMB int, siteType state.SiteType) OPcache {
	o := OPcache{MemoryMB: 128, InternedStringsMB: 16, MaxFiles: 20000}
	switch {
	case siteMemMB >= 4096:
		o = OPcache{MemoryMB: 512, InternedStringsMB: 32, MaxFiles: 100000}
	case siteMemMB >= 2048:
		o = OPcache{MemoryMB: 256, InternedStringsMB: 24, MaxFiles: 50000}
	case siteMemMB >= 1024:
		o = OPcache{MemoryMB: 192, InternedStringsMB: 16, MaxFiles: 30000}
	}
	// WooCommerce ships far more PHP files than a brochure site.
	if siteType == state.SiteWooCommerce && o.MaxFiles < 50000 {
		o.MaxFiles = 50000
	}
	return o
}

// SizeWorkers calculates PHP-FPM worker counts from the memory budget and
// an average per-worker footprint (WordPress ≈ 64–96 MB resident).
func SizeWorkers(siteMemMB, requested int) Workers {
	const perWorkerMB = 80
	max := requested
	if max <= 0 {
		max = siteMemMB / perWorkerMB
	}
	if max < 2 {
		max = 2
	}
	if max > 64 {
		max = 64
	}
	w := Workers{
		Max:         max,
		StartServe:  (max + 3) / 4,
		MinSpare:    (max + 7) / 8,
		MaxSpare:    (max + 1) / 2,
		MaxRequests: 1000,
	}
	if w.StartServe < 1 {
		w.StartServe = 1
	}
	if w.MinSpare < 1 {
		w.MinSpare = 1
	}
	if w.MaxSpare < w.StartServe {
		w.MaxSpare = w.StartServe
	}
	return w
}

type poolVars struct {
	Site        state.Site
	Socket      string
	OPcache     OPcache
	Workers     Workers
	MemLim      int
	UploadMB    int
	ExecSec     int
	APCuMB      int
	PreloadFile string
	TmpDir      string
}

// RenderPool renders the pool file for a site. siteMemMB is the memory
// budget used for sizing (0 = fall back to 1024 MB conservative default).
func RenderPool(site state.Site, siteMemMB int) (path, content string, err error) {
	if site.SystemUser == "" || site.PHPVersion == "" {
		return "", "", fmt.Errorf("site %q missing system user or php version", site.Domain)
	}
	if siteMemMB <= 0 {
		siteMemMB = 1024
	}
	memLim := 256
	if site.Type == state.SiteWooCommerce {
		memLim = 512
	}
	uploadMB := 64
	execSec := 120
	// Curated per-site overrides win when set.
	if site.Config.PHP.MemoryLimitMB > 0 {
		memLim = site.Config.PHP.MemoryLimitMB
	}
	if site.Config.PHP.UploadMaxMB > 0 {
		uploadMB = site.Config.PHP.UploadMaxMB
	}
	if site.Config.PHP.MaxExecutionSeconds > 0 {
		execSec = site.Config.PHP.MaxExecutionSeconds
	}
	// APCu shared-memory pool: a share of the site budget, capped so it
	// never dominates a small box.
	apcuMB := siteMemMB / 8
	if apcuMB < 32 {
		apcuMB = 32
	}
	if apcuMB > 256 {
		apcuMB = 256
	}
	// OPcache preload is intentionally NOT auto-enabled: live benchmarking on
	// a config-cached Laravel app showed a marginal, noise-level gain
	// (~3ms) against real cost — it can fatal PHP-FPM if the file is missing
	// and must be regenerated on every deploy. Left as an explicit opt-in.
	preload := ""
	v := poolVars{
		Site:        site,
		Socket:      SocketFor(site.SystemUser),
		OPcache:     SizeOPcache(siteMemMB, site.Type),
		Workers:     SizeWorkers(siteMemMB, site.Config.Resources.PHPWorkers),
		MemLim:      memLim,
		UploadMB:    uploadMB,
		ExecSec:     execSec,
		APCuMB:      apcuMB,
		PreloadFile: preload,
		TmpDir:      filepath.Join(site.RootPath, "tmp"),
	}
	var buf bytes.Buffer
	if err := poolTmpl.Execute(&buf, v); err != nil {
		return "", "", err
	}
	return filepath.Join(PoolDirFor(site.PHPVersion), site.SystemUser+".conf"), buf.String(), nil
}

var poolTmpl = template.Must(template.New("pool").Parse(
	`; Managed by Slipstream — do not edit. Changes are detected as drift.
; Site: {{.Site.Domain}} (id {{.Site.ID}})

[{{.Site.SystemUser}}]
user = {{.Site.SystemUser}}
group = {{.Site.SystemUser}}
listen = {{.Socket}}
listen.owner = www-data
listen.group = www-data
listen.mode = 0660

pm = dynamic
pm.max_children = {{.Workers.Max}}
pm.start_servers = {{.Workers.StartServe}}
pm.min_spare_servers = {{.Workers.MinSpare}}
pm.max_spare_servers = {{.Workers.MaxSpare}}
pm.max_requests = {{.Workers.MaxRequests}}
pm.status_path = /__slipstream/fpm-status

request_terminate_timeout = 120s
catch_workers_output = yes

; Site isolation
php_admin_value[open_basedir] = {{.Site.RootPath}}:/tmp:/usr/share/php
php_admin_value[upload_tmp_dir] = {{.TmpDir}}
php_admin_value[sys_temp_dir] = {{.TmpDir}}
php_admin_value[session.save_path] = {{.TmpDir}}/sessions
php_admin_value[error_log] = {{.Site.RootPath}}/logs/php-error.log
php_admin_flag[log_errors] = on
php_admin_flag[display_errors] = off
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen

; Resource limits
php_admin_value[memory_limit] = {{.MemLim}}M
php_admin_value[upload_max_filesize] = {{.UploadMB}}M
php_admin_value[post_max_size] = {{.UploadMB}}M
php_admin_value[max_execution_time] = {{.ExecSec}}

; APCu object cache: in-process shared memory. For a single server this is
; faster and lighter than Redis (no daemon, no socket, no serialization).
php_admin_flag[apc.enabled] = on
php_admin_flag[apc.enable_cli] = off
php_admin_value[apc.shm_size] = {{.APCuMB}}M

; Velocity Engine: OPcache sized for this site's memory budget
php_admin_flag[opcache.enable] = on{{if .PreloadFile}}
php_admin_value[opcache.preload] = {{.PreloadFile}}
php_admin_value[opcache.preload_user] = {{.Site.SystemUser}}{{end}}
php_admin_value[opcache.memory_consumption] = {{.OPcache.MemoryMB}}
php_admin_value[opcache.interned_strings_buffer] = {{.OPcache.InternedStringsMB}}
php_admin_value[opcache.max_accelerated_files] = {{.OPcache.MaxFiles}}
php_admin_value[opcache.validate_timestamps] = 1
php_admin_value[opcache.revalidate_freq] = 60
; OPcache JIT (tracing) — compiles hot PHP to native code. Off by default in
; PHP 8.x (jit_buffer_size=0). Measurable win on compute-heavy WooCommerce and
; theme rendering; negligible cost for cached hits (they never reach PHP).
php_admin_value[opcache.jit] = tracing
php_admin_value[opcache.jit_buffer_size] = 128M
php_admin_flag[opcache.enable_cli] = off
php_admin_value[realpath_cache_size] = 4096K
php_admin_value[realpath_cache_ttl] = 600

; Pre-compress at the source: PHP gzips its own output so the Velocity Engine
; page cache stores an already-compressed body and serves cache hits with no
; per-request compression CPU. Uncacheable/dynamic responses are compressed
; here too (same work nginx would otherwise do), keeping one code path.
php_admin_flag[zlib.output_compression] = on
php_admin_value[zlib.output_compression_level] = 5
`))
