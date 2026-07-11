package agent

import (
	"context"
	"fmt"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// IssueCertificate obtains (or renews) a Let's Encrypt certificate via
// certbot's webroot flow, then re-renders the vhost so Nginx switches from
// the fallback certificate to the real one.
func (a *Agent) IssueCertificate(p rpc.CertificateParams) (rpc.ApplyResult, error) {
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
