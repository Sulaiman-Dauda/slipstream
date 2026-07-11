// Package engine abstracts the web-serving stack. Slipstream describes a
// site declaratively; an engine renders that description into concrete
// configuration files. Nginx + PHP-FPM is the default engine; the
// abstraction keeps the door open for alternatives (Caddy/FrankenPHP)
// without touching the rest of the panel.
package engine

import (
	"github.com/slipstream-panel/slipstream/internal/state"
	"github.com/slipstream-panel/slipstream/internal/velocity"
)

// Input is everything an engine needs to render one site.
type Input struct {
	Site   state.Site
	Policy velocity.CachePolicy

	// DocRoot is the absolute path Nginx serves (…/current/<public_root>).
	DocRoot string
	// PHPSocket is the site's PHP-FPM socket path (empty for static/proxy).
	PHPSocket string
	// CacheDir is the site's page-cache directory.
	CacheDir string
	// LogDir receives access/error logs.
	LogDir string

	// TLS certificate paths. When CertAvailable is false the engine renders
	// the self-signed bootstrap certificate so the vhost is valid before
	// ACME issuance completes.
	CertAvailable  bool
	CertFullchain  string
	CertKey        string
	FallbackCert   string
	FallbackKey    string
	ACMEWebroot    string
	ClientMaxBody  string // e.g. "128m"
}

// Renderer renders configuration files for an engine.
type Renderer interface {
	// SiteFiles returns absolute path → file content for one site.
	SiteFiles(in Input) (map[string]string, error)
	// GlobalFiles returns engine-wide managed files (log formats, defaults).
	GlobalFiles() map[string]string
	// Name identifies the engine.
	Name() state.Engine
}
