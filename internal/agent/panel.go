package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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

	// nginx terminates the public HTTPS connection using panel.pem, but it
	// reads the certificate into worker memory at reload time — writing the
	// new file does nothing until nginx is reloaded. Without this the panel
	// keeps presenting the old (self-signed) certificate until some unrelated
	// reload happens.
	if err := a.reloadNginx(); err != nil {
		return nil, fmt.Errorf("reload nginx with new panel certificate: %w", err)
	}

	// The API reads its own TLS material once at startup (for direct :port
	// access), so it must restart to present the new certificate there too.
	// Do it DETACHED: restarting slipstream-api from inside this RPC would
	// kill the very API call that is awaiting our response, and the task would
	// be recorded as "failed: interrupted by panel restart" even though the
	// certificate was issued and installed successfully. Handing the restart
	// to a transient systemd unit lets this RPC return first; the public
	// endpoint is already live on the new cert via the nginx reload above.
	a.Runner.Run(ctx, "systemd-run", "--collect", "--unit=slipstream-panel-cert-restart",
		"/bin/sh", "-c", "sleep 2; systemctl restart slipstream-api")
	return map[string]string{"domain": p.Domain, "status": "issued"}, nil
}

// SelfUpdate downloads verified replacement binaries and swaps them in.
// Binaries are fetched to a temp path, checksum-verified, moved into place,
// and services restarted (agent last, so the API restart happens under the
// old agent and the new agent starts fresh).
// firstField returns the first whitespace-delimited token of s (e.g. the hex
// digest from "<hash>  <filename>" sha256sum output), or "".
func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// log falls back to the default logger when the agent was built without one,
// which is the case in tests.
func (a *Agent) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// defaultUpdateRepo is the repository whose signed build attestations the
// binaries are checked against.
const defaultUpdateRepo = "Sulaiman-Dauda/slipstream"

// verifyProvenance checks each staged binary against the repository's signed
// build attestations. Absence of the verifier is a refusal, not a pass, unless
// the caller said otherwise.
func (a *Agent) verifyProvenance(ctx context.Context, staged map[string]string, allowUnattested bool) error {
	repo := defaultUpdateRepo
	if _, err := exec.LookPath("gh"); err != nil {
		if allowUnattested {
			a.log().Warn("installing an update without verifying build provenance",
				"reason", "no gh on this host", "repo", repo)
			return nil
		}
		return fmt.Errorf("cannot verify build provenance: the GitHub CLI is not installed, " +
			"so the update was refused. Install gh, or accept unverified binaries explicitly")
	}
	for bin, tmp := range staged {
		if _, err := a.Runner.Run(ctx, "gh", "attestation", "verify", tmp, "--repo", repo); err != nil {
			return fmt.Errorf("build provenance check failed for %s: these bytes were not "+
				"produced by %s's release workflow, refusing the update: %w", bin, repo, err)
		}
	}
	a.log().Info("build provenance verified", "repo", repo, "binaries", len(staged))
	return nil
}

func (a *Agent) SelfUpdate(p rpc.SelfUpdateParams) (rpc.SelfUpdateResult, error) {
	if p.BaseURL == "" {
		return rpc.SelfUpdateResult{}, fmt.Errorf("update base URL required")
	}
	if !strings.HasPrefix(p.BaseURL, "https://") {
		return rpc.SelfUpdateResult{}, fmt.Errorf("update URL must be https")
	}
	ctx := context.Background()
	bins := []string{"panel-api", "slipctl", "panel-agent"}

	// Stage 1: download, verify and validate EVERY binary before swapping any.
	// A bad download must never leave a half-updated install.
	staged := map[string]string{}
	cleanupStaged := func() {
		for _, tmp := range staged {
			os.Remove(tmp)
			os.Remove(tmp + ".sha256")
		}
	}
	for _, bin := range bins {
		tmp := filepath.Join(a.Paths.WorkDir, bin+".new")
		if _, err := a.Runner.Run(ctx, "curl", "-fsSL", "-o", tmp, p.BaseURL+"/"+bin); err != nil {
			cleanupStaged()
			return rpc.SelfUpdateResult{}, fmt.Errorf("download %s: %w", bin, err)
		}
		// Fail CLOSED: a checksum is REQUIRED. A server that doesn't publish
		// one (or a MITM stripping it) must not result in an unverified root
		// binary being installed.
		if _, err := a.Runner.Run(ctx, "curl", "-fsSL", "-o", tmp+".sha256", p.BaseURL+"/"+bin+".sha256"); err != nil {
			cleanupStaged()
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("no checksum published for %s — refusing unverified update", bin)
		}
		// Compare hashes directly. "sha256sum -c" matches on the filename
		// column of the .sha256 file (the published base name), which never
		// equals our temp file name — so it fails EVERY update. Parse and
		// compare the hex digest instead.
		sumOut, err := a.Runner.Run(ctx, "sha256sum", tmp)
		if err != nil {
			cleanupStaged()
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("hash %s: %w", bin, err)
		}
		pub, err := os.ReadFile(tmp + ".sha256")
		if err != nil {
			cleanupStaged()
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("read checksum for %s: %w", bin, err)
		}
		gotHash, wantHash := firstField(sumOut), firstField(string(pub))
		if len(wantHash) != 64 || !strings.EqualFold(gotHash, wantHash) {
			cleanupStaged()
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("checksum mismatch for %s", bin)
		}
		// Reject anything that is not a native executable for this machine
		// (truncated download, wrong architecture, an HTML error page) before
		// it can replace a live root binary. Skip only if file(1) is absent —
		// the checksum already guarantees integrity.
		if out, ferr := a.Runner.Run(ctx, "file", "-b", tmp); ferr == nil && (!strings.Contains(out, "ELF") || !strings.Contains(out, "x86-64")) {
			cleanupStaged()
			os.Remove(tmp)
			return rpc.SelfUpdateResult{}, fmt.Errorf("%s is not a valid x86-64 executable — refusing update", bin)
		}
		staged[bin] = tmp
	}

	// Stage 1b: prove the bytes came from the project's release workflow, not
	// merely from the release they were published in.
	//
	// The checksums above are published beside the binaries, so anyone who can
	// write a release can replace both and the check still passes: a compromised
	// account, a leaked token or a poisoned action reaches root on every server
	// that updates. A provenance attestation is signed by GitHub's OIDC identity
	// for this workflow at a specific commit and stored outside the release, so
	// it cannot be forged by rewriting the release.
	//
	// This fails closed. A verification that runs and fails aborts the update
	// always; a box with no GitHub CLI to verify with aborts unless the caller
	// explicitly accepted that, which the panel records in the audit log.
	if err := a.verifyProvenance(ctx, staged, p.AllowUnattested); err != nil {
		cleanupStaged()
		return rpc.SelfUpdateResult{}, err
	}

	// Stage 2: back up the live binaries so we can roll back, then swap.
	backups := map[string]string{}
	restore := func() {
		for bin, bak := range backups {
			a.Runner.Run(ctx, "install", "-m", "0755", bak, "/usr/local/bin/"+bin)
		}
	}
	for _, bin := range bins {
		live := "/usr/local/bin/" + bin
		bak := live + ".bak"
		if _, err := a.Runner.Run(ctx, "cp", "-a", live, bak); err == nil {
			backups[bin] = bak
		}
	}
	for bin, tmp := range staged {
		if _, err := a.Runner.Run(ctx, "install", "-m", "0755", tmp, "/usr/local/bin/"+bin); err != nil {
			restore()
			cleanupStaged()
			return rpc.SelfUpdateResult{}, fmt.Errorf("install %s failed, rolled back: %w", bin, err)
		}
		os.Remove(tmp)
		os.Remove(tmp + ".sha256")
	}

	// Stage 3: hand the restart + health-gate + rollback to a DETACHED guard.
	// It cannot run inline: restarting slipstream-api tears down our RPC caller
	// and restarting the agent tears down this process, so the verify-and-roll-
	// back must outlive both. The guard restarts the API on the new binary,
	// polls /healthz, and if it does not come up, reinstalls the .bak binaries
	// and restarts — so a broken build never leaves the panel down.
	guard := filepath.Join(a.Paths.WorkDir, "update-guard.sh")
	if err := os.WriteFile(guard, []byte(updateGuardScript), 0o700); err != nil {
		restore()
		cleanupStaged()
		return rpc.SelfUpdateResult{}, fmt.Errorf("write update guard: %w", err)
	}
	// --collect garbage-collects the transient unit after it exits; no --unit so
	// concurrent runs can't collide on a fixed name.
	if _, err := a.Runner.Run(ctx, "systemd-run", "--collect", "/bin/bash", guard); err != nil {
		restore()
		a.Runner.Run(ctx, "systemctl", "reset-failed", "slipstream-api")
		a.Runner.Run(ctx, "systemctl", "restart", "slipstream-api")
		return rpc.SelfUpdateResult{}, fmt.Errorf("launch update guard failed, rolled back: %w", err)
	}
	return rpc.SelfUpdateResult{UpdatedTo: p.Version, Restarted: true}, nil
}

// updateGuardScript restarts panel-api on the freshly installed binaries,
// health-gates it, and rolls back to the .bak binaries if it does not come up.
// It runs as a detached systemd transient unit so it survives the restarts of
// both panel-api (the update's RPC caller) and the agent.
const updateGuardScript = `#!/bin/bash
set -u
HEALTH="https://127.0.0.1:5252/healthz"
systemctl reset-failed slipstream-api 2>/dev/null
systemctl restart slipstream-api
ok=0
for i in $(seq 1 20); do
  sleep 1
  [ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 3 "$HEALTH" 2>/dev/null)" = "200" ] && { ok=1; break; }
done
if [ "$ok" != "1" ]; then
  for b in panel-api slipctl panel-agent; do
    [ -f "/usr/local/bin/$b.bak" ] && install -m0755 "/usr/local/bin/$b.bak" "/usr/local/bin/$b"
  done
  systemctl reset-failed slipstream-api slipstream-agent 2>/dev/null
  systemctl restart slipstream-agent
  systemctl restart slipstream-api
else
  systemctl restart slipstream-agent
fi
`
