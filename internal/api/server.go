// Package api is the unprivileged HTTP control plane: authentication,
// desired-state management, task orchestration, and the embedded UI. All
// privileged work is delegated to the agent over RPC.
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// AgentCaller is the agent RPC surface the API uses (an interface so tests
// can fake the agent).
type AgentCaller interface {
	Call(method string, params any, out any) error
}

// Server owns the HTTP API.
type Server struct {
	Store *state.Store
	// updates caches the newest published release so opening the dashboard
	// does not mean a request to GitHub every time.
	updates updateCache
	Agent AgentCaller
	Log   *slog.Logger
	// UI is the embedded frontend (may be nil in tests).
	UI fs.FS
	// InsecureCookies disables the Secure cookie flag for local development.
	InsecureCookies bool
	// DefaultPHP is the PHP version new sites get (set from the installed
	// runtime; empty falls back to 8.4).
	DefaultPHP string
	// PanelPort is the HTTPS port the panel listens on (for redirects and
	// panel-cert issuance).
	PanelPort int
	// Shutdown is closed when graceful process shutdown begins. Long-lived
	// handlers use it to exit promptly instead of consuming the drain timeout.
	Shutdown <-chan struct{}

	loginLimiter *rateLimiter
}

// Init prepares runtime state (rate limiter) and reconciles tasks left
// 'running' by a previous crash/restart. Idempotent.
func (s *Server) Init() {
	if s.loginLimiter == nil {
		s.loginLimiter = newRateLimiter(8, time.Minute)
		if s.Store != nil {
			s.Store.ReconcileRunningTasks()
		}
	}
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Routes builds the full handler tree.
func (s *Server) Routes() http.Handler {
	s.Init()
	mux := http.NewServeMux()

	// Unauthenticated.
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)

	// Connector endpoints authenticate with per-site bearer tokens.
	mux.HandleFunc("POST /api/connector/purge", s.handleConnectorPurge)

	// auth = any signed-in user (read-only included). admin = full-access
	// accounts only; read-only accounts can view but not change.
	auth := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireSession(fn) }
	admin := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireAdmin(fn) }
	manage := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireSiteManager(fn) }
	global := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireGlobalViewer(fn) }

	mux.HandleFunc("POST /api/logout", auth(s.handleLogout))
	mux.HandleFunc("GET /api/me", auth(s.handleMe))

	// Account & security (a user may always manage their own account)
	mux.HandleFunc("POST /api/account/password", auth(s.handleChangePassword))
	mux.HandleFunc("POST /api/account/2fa/begin", auth(s.handleTOTPBegin))
	mux.HandleFunc("POST /api/account/2fa/confirm", auth(s.handleTOTPConfirm))
	mux.HandleFunc("POST /api/account/2fa/disable", auth(s.handleTOTPDisable))
	mux.HandleFunc("GET /api/account/sessions", auth(s.handleListSessions))
	mux.HandleFunc("DELETE /api/account/sessions/{id}", auth(s.handleRevokeSession))
	mux.HandleFunc("GET /api/users", global(s.handleListUsers))
	mux.HandleFunc("POST /api/users", admin(s.handleCreateUser))
	mux.HandleFunc("DELETE /api/users/{id}", admin(s.handleDeleteUser))

	// Cron
	mux.HandleFunc("GET /api/sites/{id}/cron", auth(s.handleListCron))
	mux.HandleFunc("POST /api/sites/{id}/cron", manage(s.handleCreateCron))
	mux.HandleFunc("DELETE /api/cron/{id}", manage(s.handleDeleteCron))
	mux.HandleFunc("POST /api/cron/{id}/run", manage(s.handleRunCron))

	// Database tools
	mux.HandleFunc("GET /api/sites/{id}/database", auth(s.handleDatabaseInfo))
	mux.HandleFunc("POST /api/sites/{id}/database/query", manage(s.handleDatabaseQuery))
	mux.HandleFunc("POST /api/sites/{id}/database/export", manage(s.handleDatabaseExport))
	mux.HandleFunc("POST /api/sites/{id}/database/import", manage(s.handleDatabaseImport))
	mux.HandleFunc("POST /api/sites/{id}/database/adminer", manage(s.handleLaunchAdminer))
	mux.HandleFunc("POST /api/sites/{id}/migration", manage(s.handleImportMigration))

	// Files
	mux.HandleFunc("GET /api/sites/{id}/files", auth(s.handleListFiles))
	mux.HandleFunc("GET /api/sites/{id}/files/read", auth(s.handleReadFile))
	mux.HandleFunc("POST /api/sites/{id}/files/write", manage(s.handleWriteFile))
	mux.HandleFunc("GET /api/sites/{id}/files/download", auth(s.handleDownloadFile))
	mux.HandleFunc("POST /api/sites/{id}/files/upload", manage(s.handleUploadFile))
	mux.HandleFunc("POST /api/sites/{id}/files/manage", manage(s.handleManageFile))
	mux.HandleFunc("POST /api/sites/{id}/sftp", manage(s.handleSetSFTP))
	mux.HandleFunc("GET /api/sites/{id}/ssh-keys", auth(s.handleListSSHKeys))
	mux.HandleFunc("POST /api/sites/{id}/ssh-keys", manage(s.handleAddSSHKey))
	mux.HandleFunc("DELETE /api/sites/{id}/ssh-keys/{fingerprint}", manage(s.handleDeleteSSHKey))

	// PHP settings
	mux.HandleFunc("PUT /api/sites/{id}/php", manage(s.handlePHPSettings))

	// WordPress toolkit
	mux.HandleFunc("POST /api/sites/{id}/wp/login", manage(s.handleWPMagicLogin))
	mux.HandleFunc("GET /api/sites/{id}/wp/plugins", auth(s.handleWPPlugins))
	mux.HandleFunc("POST /api/sites/{id}/wp/update", manage(s.handleWPUpdate))
	mux.HandleFunc("POST /api/sites/{id}/wp/object-cache", manage(s.handleWPObjectCache))
	mux.HandleFunc("GET /api/sites/{id}/cache-stats", auth(s.handleCacheStats))
	mux.HandleFunc("POST /api/sites/{id}/warm", manage(s.handleWarmCache))

	// Services, firewall, panel cert
	mux.HandleFunc("GET /api/services", global(s.handleListServices))
	mux.HandleFunc("POST /api/services/{name}/restart", admin(s.handleRestartService))
	mux.HandleFunc("GET /api/logs", auth(s.handleReadLog))
	mux.HandleFunc("GET /api/firewall", global(s.handleFirewallStatus))
	mux.HandleFunc("POST /api/firewall/rule", admin(s.handleFirewallRule))
	mux.HandleFunc("POST /api/panel/certificate", admin(s.handlePanelCertificate))
	mux.HandleFunc("POST /api/panel/update", admin(s.handleSelfUpdate))

	mux.HandleFunc("GET /api/sites", auth(s.handleListSites))
	mux.HandleFunc("POST /api/sites", admin(s.handleCreateSite))
	mux.HandleFunc("GET /api/sites/{id}", auth(s.handleGetSite))
	mux.HandleFunc("DELETE /api/sites/{id}", admin(s.handleDeleteSite))
	mux.HandleFunc("POST /api/sites/{id}/purge", manage(s.handlePurge))
	mux.HandleFunc("POST /api/sites/{id}/certificate", manage(s.handleIssueCertificate))
	mux.HandleFunc("POST /api/sites/{id}/staging", manage(s.handleCreateStaging))
	mux.HandleFunc("PUT /api/sites/{id}/config", manage(s.handleUpdateSiteConfig))

	mux.HandleFunc("GET /api/sites/{id}/deployments", auth(s.handleListDeployments))
	mux.HandleFunc("POST /api/sites/{id}/deployments", manage(s.handleDeploy))
	mux.HandleFunc("POST /api/sites/{id}/safe-push", manage(s.handleSafePush))
	mux.HandleFunc("GET /api/sites/{id}/staging/tables", auth(s.handleStagingTables))
	mux.HandleFunc("POST /api/sites/{id}/staging/database", manage(s.handlePushStagingDatabase))
	mux.HandleFunc("POST /api/deployments/{id}/promote", manage(s.handlePromote))
	mux.HandleFunc("POST /api/sites/{id}/rollback", manage(s.handleRollback))

	mux.HandleFunc("GET /api/sites/{id}/backups", auth(s.handleListBackups))
	mux.HandleFunc("POST /api/sites/{id}/backups", manage(s.handleRunBackup))
	mux.HandleFunc("POST /api/backups/{id}/verify", manage(s.handleVerifyBackup))
	mux.HandleFunc("POST /api/backups/{id}/restore", manage(s.handleRestoreBackup))
	mux.HandleFunc("POST /api/backups/test", admin(s.handleTestBackupRepo))

	mux.HandleFunc("GET /api/tasks", auth(s.handleListTasks))
	mux.HandleFunc("GET /api/tasks/{id}", auth(s.handleGetTask))
	mux.HandleFunc("GET /api/events", auth(s.handleEvents))

	mux.HandleFunc("GET /api/system/status", auth(s.handleSystemStatus))
	mux.HandleFunc("GET /api/system/update", auth(s.handleUpdateStatus))
	mux.HandleFunc("GET /api/system/metrics", auth(s.handleSystemMetrics))
	mux.HandleFunc("GET /api/system/drift", global(s.handleListDrift))
	mux.HandleFunc("POST /api/system/drift/{id}/resolve", admin(s.handleResolveDrift))
	mux.HandleFunc("GET /api/audit", global(s.handleAudit))
	mux.HandleFunc("GET /api/settings", global(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", admin(s.handlePutSettings))

	// Unauthenticated liveness probe for monitors/load balancers.
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Everything else: the embedded single-page UI.
	mux.HandleFunc("/", s.handleUI)

	return s.withCommonHeaders(mux)
}

// ConnectorRoutes is the minimal surface exposed on the plaintext loopback
// listener: only the WordPress connector's purge endpoint (bearer-token
// authenticated). The full admin API and UI are served ONLY over TLS, so a
// local process (e.g. a compromised site's PHP) cannot reach panel
// endpoints it has no business touching.
func (s *Server) ConnectorRoutes() http.Handler {
	s.Init()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/connector/purge", s.handleConnectorPurge)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s.withCommonHeaders(mux)
}

func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; font-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if s.UI == nil {
		http.Error(w, "ui not embedded", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(s.UI, path); err != nil {
		// SPA fallback: unknown paths render the app shell.
		path = "index.html"
	}
	http.ServeFileFS(w, r, s.UI, path)
}

// --- helpers ---

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func respondErr(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}

func decode[T any](r *http.Request, into *T) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

// randomToken returns a URL-safe random secret.
func randomToken(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// siteOr404 loads a site or writes the error response.
func (s *Server) siteOr404(w http.ResponseWriter, r *http.Request) (state.Site, bool) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return state.Site{}, false
	}
	site, err := s.Store.GetSite(id)
	if errors.Is(err, state.ErrNotFound) {
		respondErr(w, http.StatusNotFound, "site not found")
		return state.Site{}, false
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return state.Site{}, false
	}
	if !s.canAccessSite(r, site.ID) {
		respondErr(w, http.StatusNotFound, "site not found")
		return state.Site{}, false
	}
	return site, true
}
