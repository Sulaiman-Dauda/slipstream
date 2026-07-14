package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// PanelCertificate issues a Let's Encrypt certificate for the panel's own
// domain and installs it as the panel TLS material, then restarts the API
// so it serves the new certificate. Uses certbot standalone on port 80 is
// unavailable (nginx owns it), so it uses the webroot flow through a
// dedicated panel challenge location served by nginx.
func (a *Agent) PanelCertificate(p rpc.PanelCertParams) (map[string]string, error) {
	a.certMu.Lock()
	defer a.certMu.Unlock()
	if err := nginx.ValidateDomain(p.Domain); err != nil {
		return nil, err
	}
	if p.Email == "" {
		return nil, fmt.Errorf("acme email required")
	}
	ctx := context.Background()

	// Answer ACME challenges over port 80 and expose the panel on standard
	// HTTPS. The API keeps its own TLS listener on the configured panel port;
	// Nginx terminates the public connection and proxies to that listener.
	vhost := fmt.Sprintf(`# Managed by Slipstream — panel domain %s
server {
    listen 80;
    listen [::]:80;
    server_name %s;
    location /.well-known/acme-challenge/ { root %s; }
	location / { return 301 https://$host$request_uri; }
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name %s;
    server_tokens off;
    ssl_certificate /etc/slipstream/certs/panel.pem;
    ssl_certificate_key /etc/slipstream/certs/panel.key;
    add_header Strict-Transport-Security "max-age=31536000" always;
    location / {
        proxy_pass https://127.0.0.1:%d;
        proxy_ssl_verify off;
        proxy_buffering off;
        proxy_read_timeout 1h;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
    }
}
`, p.Domain, p.Domain, a.Paths.ACMEWebroot, p.Domain, p.Port)
	vpath := filepath.Join(a.Paths.NginxSites, "slipstream-panel.conf")
	if _, err := writeManaged(vpath, vhost, 0o644); err != nil {
		return nil, err
	}
	if err := a.reloadNginx(); err != nil {
		return nil, err
	}
	if err := a.preflightHTTP01(ctx, []string{p.Domain}); err != nil {
		return nil, err
	}

	if _, err := a.Runner.Run(ctx, "certbot", "certonly", "--webroot",
		"--webroot-path", a.Paths.ACMEWebroot, "--non-interactive", "--agree-tos",
		"--email", p.Email, "--cert-name", "slipstream-panel", "-d", p.Domain); err != nil {
		return nil, fmt.Errorf("panel certificate issuance: %w", err)
	}

	// Install the issued cert as the panel TLS material.
	live := filepath.Join(a.Paths.CertLiveDir, "slipstream-panel")
	fullchain, err := os.ReadFile(filepath.Join(live, "fullchain.pem"))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(live, "privkey.pem"))
	if err != nil {
		return nil, err
	}
	// Only rewrite + restart when the certificate actually changed — certbot
	// no-ops when a cert is not yet due, and restarting the API on every
	// renewal check (every 12h) would needlessly drop connections.
	existing, _ := os.ReadFile("/etc/slipstream/certs/panel.pem")
	if string(existing) == string(fullchain) {
		return map[string]string{"domain": p.Domain, "status": "unchanged"}, nil
	}
	if err := os.WriteFile("/etc/slipstream/certs/panel.pem", fullchain, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile("/etc/slipstream/certs/panel.key", key, 0o640); err != nil {
		return nil, err
	}
	a.Runner.Run(ctx, "chgrp", "slipstream", "/etc/slipstream/certs/panel.key", "/etc/slipstream/certs/panel.pem")

	// Restart the API so it picks up the new certificate.
	a.Runner.Run(ctx, "systemctl", "restart", "slipstream-api")
	return map[string]string{"domain": p.Domain, "status": "issued"}, nil
}

// SelfUpdate downloads verified replacement binaries and swaps them in.
// Binaries are fetched to a temp path, checksum-verified, moved into place,
// and services restarted (agent last, so the API restart happens under the
// old agent and the new agent starts fresh).
func (a *Agent) SelfUpdate(p rpc.SelfUpdateParams) (rpc.SelfUpdateResult, error) {
	if p.BaseURL == "" {
		return rpc.SelfUpdateResult{}, fmt.Errorf("update base URL required")
	}
	if !strings.HasPrefix(p.BaseURL, "https://") {
		return rpc.SelfUpdateResult{}, fmt.Errorf("update URL must be https")
	}
	ctx := context.Background()
	for _, bin := range []string{"panel-api", "slipctl", "panel-agent"} {
		tmp := filepath.Join(a.Paths.WorkDir, bin+".new")
		if _, err := a.Runner.Run(ctx, "curl", "-fsSL", "-o", tmp, p.BaseURL+"/"+bin); err != nil {
			return rpc.SelfUpdateResult{}, fmt.Errorf("download %s: %w", bin, err)
		}
		// Fail CLOSED: a checksum is REQUIRED. A server that doesn't publish
		// one (or a MITM stripping it) must not result in an unverified root
		// binary being installed.
		if _, err := a.Runner.Run(ctx, "curl", "-fsSL", "-o", tmp+".sha256", p.BaseURL+"/"+bin+".sha256"); err != nil {
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("no checksum published for %s — refusing unverified update", bin)
		}
		if _, err := a.Runner.Run(ctx, "sha256sum", "-c", tmp+".sha256"); err != nil {
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("checksum mismatch for %s", bin)
		}
		if _, err := a.Runner.Run(ctx, "install", "-m", "0755", tmp, "/usr/local/bin/"+bin); err != nil {
			return rpc.SelfUpdateResult{}, fmt.Errorf("install %s: %w", bin, err)
		}
		os.Remove(tmp)
	}
	// Restart API now; the agent restarts itself last (systemd brings it
	// back up), which also ends this RPC connection cleanly.
	a.Runner.Run(ctx, "systemctl", "restart", "slipstream-api")
	go func() {
		a.Runner.Run(context.Background(), "systemctl", "restart", "slipstream-agent")
	}()
	return rpc.SelfUpdateResult{UpdatedTo: p.Version, Restarted: true}, nil
}
