package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// preflightHTTP01 proves that the public hostname reaches this server's ACME
// webroot before Certbot consumes a rate-limited authorization attempt.
func (a *Agent) preflightHTTP01(ctx context.Context, domains []string) error {
	var secret [16]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return fmt.Errorf("generate ACME preflight token: %w", err)
	}
	token := "slipstream-" + hex.EncodeToString(secret[:])
	dir := filepath.Join(a.Paths.ACMEWebroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("prepare ACME webroot: %w", err)
	}
	// os.MkdirAll and os.WriteFile honor the process umask, and the systemd
	// units run the agent with UMask=0027 — which strips the world bits nginx
	// (www-data) needs to traverse the challenge directory and read the token,
	// making every HTTP-01 validation return 403. Force readable perms.
	_ = os.Chmod(filepath.Join(a.Paths.ACMEWebroot, ".well-known"), 0o755)
	_ = os.Chmod(dir, 0o755)
	probe := filepath.Join(dir, token)
	if err := os.WriteFile(probe, []byte(token), 0o644); err != nil {
		return fmt.Errorf("write ACME preflight: %w", err)
	}
	_ = os.Chmod(probe, 0o644)
	defer os.Remove(probe)

	client := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, domain := range domains {
		url := "http://" + domain + "/.well-known/acme-challenge/" + token
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("ACME preflight for %s failed: %v%s", domain, err, dnsHint(domain))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != token {
			return fmt.Errorf("ACME preflight for %s failed: public challenge returned HTTP %d%s; point its A/AAAA records to this server and disable any proxy that intercepts /.well-known/acme-challenge/", domain, resp.StatusCode, dnsHint(domain))
		}
	}
	return nil
}

func dnsHint(domain string) string {
	addrs, err := net.LookupHost(domain)
	if err != nil || len(addrs) == 0 {
		return " (DNS does not resolve)"
	}
	return " (DNS resolves to " + strings.Join(addrs, ", ") + ")"
}

// IssueCertificate obtains (or renews) a Let's Encrypt certificate via
// certbot's webroot flow, then re-renders the vhost so Nginx switches from
// the fallback certificate to the real one.
func (a *Agent) IssueCertificate(p rpc.CertificateParams) (rpc.ApplyResult, error) {
	a.certMu.Lock()
	defer a.certMu.Unlock()
	ctx := context.Background()
	site := p.Site
	if err := validateSite(site); err != nil {
		return rpc.ApplyResult{}, err
	}
	if p.Email == "" {
		return rpc.ApplyResult{}, fmt.Errorf("acme account email required")
	}

	args := []string{
		"certonly", "--webroot", "--webroot-path", a.Paths.ACMEWebroot,
		"--non-interactive", "--agree-tos", "--email", p.Email,
		"--cert-name", site.Domain,
		"-d", site.Domain,
	}
	for _, alias := range site.Aliases {
		args = append(args, "-d", alias)
	}
	if err := a.preflightHTTP01(ctx, append([]string{site.Domain}, site.Aliases...)); err != nil {
		return rpc.ApplyResult{}, err
	}
	if p.Staging {
		args = append(args, "--staging")
	}
	if _, err := a.Runner.Run(ctx, "certbot", args...); err != nil {
		return rpc.ApplyResult{}, fmt.Errorf("certificate issuance: %w", err)
	}

	// Re-render: the vhost now points at the issued certificate.
	files, err := a.renderSite(ctx, site)
	if err != nil {
		return rpc.ApplyResult{}, err
	}
	return rpc.ApplyResult{Files: files}, nil
}
