// Package nginx renders Slipstream's default engine: Nginx vhosts with
// Velocity Engine FastCGI page caching, plus per-site PHP-FPM upstreams.
package nginx

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/slipstream-panel/slipstream/internal/engine"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// Renderer implements engine.Renderer for Nginx + PHP-FPM. The output
// directories are configurable so tests can render into a temp tree; empty
// fields use the production defaults.
type Renderer struct {
	SitesDir string // default /etc/nginx/sites-enabled
	ConfDir  string // default /etc/nginx/conf.d
}

// Name identifies the engine.
func (Renderer) Name() state.Engine { return state.EngineNginx }

// DefaultSitesDir is where per-site vhosts are written.
const DefaultSitesDir = "/etc/nginx/sites-enabled"

// DefaultConfDir is where http-level snippets (cache zones) are written.
const DefaultConfDir = "/etc/nginx/conf.d"

func (r Renderer) sitesDir() string {
	if r.SitesDir != "" {
		return r.SitesDir
	}
	return DefaultSitesDir
}

func (r Renderer) confDir() string {
	if r.ConfDir != "" {
		return r.ConfDir
	}
	return DefaultConfDir
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// ValidateDomain rejects anything that is not a plain lowercase hostname.
// This is a security boundary: domains are interpolated into config files.
func ValidateDomain(d string) error {
	if len(d) > 253 || !domainRe.MatchString(d) {
		return fmt.Errorf("invalid domain %q", d)
	}
	return nil
}

type siteVars struct {
	engine.Input
	ZoneName    string
	SkipVar     string
	ServerNames string
	InactiveSec int
	IsPHP       bool
	IsProxy     bool
	IsStatic    bool
	CookieRe    string
	URIRe       string
	Cert        string
	Key         string
}

// SiteFiles renders the vhost and (when caching is on) the http-level cache
// zone for one site.
func (r Renderer) SiteFiles(in engine.Input) (map[string]string, error) {
	site := in.Site
	if err := ValidateDomain(site.Domain); err != nil {
		return nil, err
	}
	for _, a := range site.Aliases {
		if err := ValidateDomain(a); err != nil {
			return nil, err
		}
	}
	if in.ClientMaxBody == "" {
		in.ClientMaxBody = "128m"
	}

	v := siteVars{
		Input:       in,
		ZoneName:    fmt.Sprintf("slip_%d", site.ID),
		SkipVar:     fmt.Sprintf("$slip_skip_%d", site.ID),
		ServerNames: strings.Join(append([]string{site.Domain}, site.Aliases...), " "),
		InactiveSec: in.Policy.TTLSec + in.Policy.StaleSec,
		IsPHP:       in.PHPSocket != "" && site.Type != state.SiteProxy && site.Type != state.SiteStatic,
		IsProxy:     site.Type == state.SiteProxy,
		IsStatic:    site.Type == state.SiteStatic,
	}
	if in.CertAvailable {
		v.Cert, v.Key = in.CertFullchain, in.CertKey
	} else {
		v.Cert, v.Key = in.FallbackCert, in.FallbackKey
	}
	if len(in.Policy.BypassCookies) > 0 {
		v.CookieRe = strings.Join(escapeAll(in.Policy.BypassCookies), "|")
	}
	if len(in.Policy.BypassURIPrefixes) > 0 {
		prefixes := make([]string, 0, len(in.Policy.BypassURIPrefixes))
		for _, p := range in.Policy.BypassURIPrefixes {
			prefixes = append(prefixes, "^"+regexp.QuoteMeta(p))
		}
		v.URIRe = strings.Join(prefixes, "|")
	}

	files := map[string]string{}
	vhost, err := render(vhostTmpl, v)
	if err != nil {
		return nil, err
	}
	files[filepath.Join(r.sitesDir(), site.Domain+".conf")] = vhost

	if in.Policy.Enabled {
		zone, err := render(zoneTmpl, v)
		if err != nil {
			return nil, err
		}
		files[filepath.Join(r.confDir(), fmt.Sprintf("slipstream-cache-%d.conf", site.ID))] = zone
	}
	return files, nil
}

// GlobalFiles renders engine-wide managed configuration.
func (r Renderer) GlobalFiles() map[string]string {
	return map[string]string{
		filepath.Join(r.confDir(), "slipstream-global.conf"): globalConf,
	}
}

func escapeAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

func render(t *template.Template, v siteVars) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

var globalConf = `# Managed by Slipstream — do not edit. Changes are detected as drift.
log_format slipstream '$remote_addr - $host [$time_local] "$request" '
                      '$status $body_bytes_sent $request_time '
                      'cache:$upstream_cache_status "$http_user_agent"';


# gzip itself is enabled by the distro nginx.conf ("gzip on;"); redeclaring
# it here would be a duplicate-directive error. These tune it.
gzip_vary on;
gzip_comp_level 5;
gzip_min_length 1024;
gzip_types text/plain text/css text/xml application/json application/javascript
           application/xml+rss application/atom+xml image/svg+xml font/woff2;
`

var zoneTmpl = template.Must(template.New("zone").Parse(
	`# Managed by Slipstream — Velocity Engine cache zone for {{.Site.Domain}} (site {{.Site.ID}}).
fastcgi_cache_path {{.CacheDir}} levels=1:2 keys_zone={{.ZoneName}}:{{.Policy.ZoneSizeMB}}m
                   max_size={{.Policy.MaxSizeMB}}m inactive={{.InactiveSec}}s use_temp_path=off;
`))

var vhostTmpl = template.Must(template.New("vhost").Parse(
	`# Managed by Slipstream — do not edit. Changes are detected as drift.
# Site: {{.Site.Domain}} (id {{.Site.ID}}, type {{.Site.Type}}, profile {{.Site.Profile}})

server {
    listen 80;
    listen [::]:80;
    server_name {{.ServerNames}};

    location /.well-known/acme-challenge/ {
        root {{.ACMEWebroot}};
    }
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name {{.ServerNames}};
    server_tokens off;

    # The stock Ubuntu nginx.conf sets "tcp_nopush on" but omits tcp_nodelay,
    # so keepalive connections stall on the final segment for the 40ms TCP
    # delayed-ACK timer — capping cached throughput ~10x below what the cache
    # can serve. Overriding here removes the stall (measured 850 -> 7800 req/s
    # on a 1-core box, matching Varnish). sendfile stays on (main config).
    tcp_nodelay on;
    tcp_nopush off;

    ssl_certificate {{.Cert}};
    ssl_certificate_key {{.Key}};
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers off;
    ssl_session_timeout 1d;
    ssl_session_cache shared:slip_ssl:10m;

    access_log {{.LogDir}}/access.log slipstream;
    error_log {{.LogDir}}/error.log warn;

    client_max_body_size {{.ClientMaxBody}};

    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
{{- if .IsProxy}}

    location / {
        proxy_pass {{.Site.Config.ProxyUpstream}};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 60s;
    }
{{- else}}

    root {{.DocRoot}};
    index index.php index.html index.htm;

    location ~ /\.(?!well-known) {
        deny all;
    }

    location ~* \.(css|js|mjs|jpg|jpeg|png|gif|webp|avif|svg|ico|woff|woff2|ttf|eot|otf|mp4|webm|pdf)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
        access_log off;
        try_files $uri =404;
    }
{{- if .IsStatic}}

    location / {
        try_files $uri $uri/ /index.html =404;
    }
{{- end}}
{{- if .IsPHP}}

    location / {
        try_files $uri $uri/ /index.php?$args;
    }
{{- if .Policy.Enabled}}

    # Velocity Engine bypass rules: logged-in users, carts, admin and
    # non-idempotent requests never touch the page cache.
    set {{.SkipVar}} 0;
    if ($request_method = POST) {
        set {{.SkipVar}} 1;
    }
    if ($query_string != "") {
        set {{.SkipVar}} 1;
    }
{{- if .CookieRe}}
    if ($http_cookie ~* "{{.CookieRe}}") {
        set {{.SkipVar}} 1;
    }
{{- end}}
{{- if .URIRe}}
    if ($request_uri ~* "{{.URIRe}}") {
        set {{.SkipVar}} 1;
    }
{{- end}}
{{- end}}

    location ~ \.php$ {
        try_files $uri =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param HTTPS on;
        fastcgi_pass unix:{{.PHPSocket}};
        fastcgi_read_timeout 120s;
        fastcgi_buffers 16 32k;
        fastcgi_buffer_size 64k;
{{- if .Policy.Enabled}}

        # Velocity Engine full-page cache.
        fastcgi_cache {{.ZoneName}};
        fastcgi_cache_key "$scheme$request_method$host$request_uri";
        fastcgi_cache_valid 200 301 {{.Policy.TTLSec}}s;
        fastcgi_cache_valid 404 60s;
        fastcgi_cache_methods GET HEAD;
        fastcgi_cache_bypass {{.SkipVar}};
        fastcgi_no_cache {{.SkipVar}};
        # Request coalescing: one regeneration per URL, others wait briefly.
        fastcgi_cache_lock on;
        fastcgi_cache_lock_timeout {{.Policy.LockTimeoutSec}}s;
        # Stale-while-revalidate and stale-if-error.
        fastcgi_cache_use_stale updating error timeout invalid_header http_500 http_503;
        fastcgi_cache_background_update on;
        fastcgi_ignore_headers Cache-Control Expires Set-Cookie;
        add_header X-Slipstream-Cache $upstream_cache_status always;
{{- end}}
    }
{{- end}}
{{- end}}
}
`))
