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

	loginLimiter *rateLimiter
}

// Init prepares runtime state (rate limiter). Call once before Routes.
func (s *Server) Init() {
	if s.loginLimiter == nil {
		s.loginLimiter = newRateLimiter(8, time.Minute)
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

	// Session-authenticated.
	auth := func(fn http.HandlerFunc) http.HandlerFunc { return s.requireSession(fn) }
	mux.HandleFunc("POST /api/logout", auth(s.handleLogout))
	mux.HandleFunc("GET /api/me", auth(s.handleMe))

	// Account & security
	mux.HandleFunc("POST /api/account/password", auth(s.handleChangePassword))
	mux.HandleFunc("POST /api/account/2fa/begin", auth(s.handleTOTPBegin))
	mux.HandleFunc("POST /api/account/2fa/confirm", auth(s.handleTOTPConfirm))
	mux.HandleFunc("POST /api/account/2fa/disable", auth(s.handleTOTPDisable))
	mux.HandleFunc("GET /api/account/sessions", auth(s.handleListSessions))
	mux.HandleFunc("DELETE /api/account/sessions/{id}", auth(s.handleRevokeSession))
	mux.HandleFunc("GET /api/users", auth(s.handleListUsers))
	mux.HandleFunc("POST /api/users", auth(s.handleCreateUser))
	mux.HandleFunc("DELETE /api/users/{id}", auth(s.handleDeleteUser))

	// Cron
	mux.HandleFunc("GET /api/sites/{id}/cron", auth(s.handleListCron))
	mux.HandleFunc("POST /api/sites/{id}/cron", auth(s.handleCreateCron))
	mux.HandleFunc("DELETE /api/cron/{id}", auth(s.handleDeleteCron))

	// Database tools
	mux.HandleFunc("GET /api/sites/{id}/database", auth(s.handleDatabaseInfo))
	mux.HandleFunc("POST /api/sites/{id}/database/query", auth(s.handleDatabaseQuery))
	mux.HandleFunc("POST /api/sites/{id}/database/export", auth(s.handleDatabaseExport))
	mux.HandleFunc("POST /api/sites/{id}/database/adminer", auth(s.handleLaunchAdminer))

	// Files
	mux.HandleFunc("GET /api/sites/{id}/files", auth(s.handleListFiles))
	mux.HandleFunc("GET /api/sites/{id}/files/read", auth(s.handleReadFile))
	mux.HandleFunc("POST /api/sites/{id}/files/write", auth(s.handleWriteFile))
	mux.HandleFunc("POST /api/sites/{id}/sftp", auth(s.handleSetSFTP))

	// PHP settings
	mux.HandleFunc("PUT /api/sites/{id}/php", auth(s.handlePHPSettings))

	// WordPress toolkit
	mux.HandleFunc("POST /api/sites/{id}/wp/login", auth(s.handleWPMagicLogin))
	mux.HandleFunc("GET /api/sites/{id}/wp/plugins", auth(s.handleWPPlugins))
	mux.HandleFunc("POST /api/sites/{id}/wp/update", auth(s.handleWPUpdate))
	mux.HandleFunc("POST /api/sites/{id}/wp/object-cache", auth(s.handleWPObjectCache))
	mux.HandleFunc("GET /api/sites/{id}/cache-stats", auth(s.handleCacheStats))
	mux.HandleFunc("POST /api/sites/{id}/warm", auth(s.handleWarmCache))

	// Services, firewall, panel cert
	mux.HandleFunc("GET /api/services", auth(s.handleListServices))
	mux.HandleFunc("POST /api/services/{name}/restart", auth(s.handleRestartService))
	mux.HandleFunc("GET /api/logs", auth(s.handleReadLog))
	mux.HandleFunc("GET /api/firewall", auth(s.handleFirewallStatus))
	mux.HandleFunc("POST /api/firewall/rule", auth(s.handleFirewallRule))
	mux.HandleFunc("POST /api/panel/certificate", auth(s.handlePanelCertificate))
	mux.HandleFunc("POST /api/panel/update", auth(s.handleSelfUpdate))

	mux.HandleFunc("GET /api/sites", auth(s.handleListSites))
	mux.HandleFunc("POST /api/sites", auth(s.handleCreateSite))
	mux.HandleFunc("GET /api/sites/{id}", auth(s.handleGetSite))
	mux.HandleFunc("DELETE /api/sites/{id}", auth(s.handleDeleteSite))
	mux.HandleFunc("POST /api/sites/{id}/purge", auth(s.handlePurge))
	mux.HandleFunc("POST /api/sites/{id}/certificate", auth(s.handleIssueCertificate))
	mux.HandleFunc("POST /api/sites/{id}/staging", auth(s.handleCreateStaging))
	mux.HandleFunc("PUT /api/sites/{id}/config", auth(s.handleUpdateSiteConfig))

	mux.HandleFunc("GET /api/sites/{id}/deployments", auth(s.handleListDeployments))
	mux.HandleFunc("POST /api/sites/{id}/deployments", auth(s.handleDeploy))
	mux.HandleFunc("POST /api/sites/{id}/safe-push", auth(s.handleSafePush))
	mux.HandleFunc("POST /api/deployments/{id}/promote", auth(s.handlePromote))
	mux.HandleFunc("POST /api/sites/{id}/rollback", auth(s.handleRollback))

	mux.HandleFunc("GET /api/sites/{id}/backups", auth(s.handleListBackups))
	mux.HandleFunc("POST /api/sites/{id}/backups", auth(s.handleRunBackup))
	mux.HandleFunc("POST /api/backups/{id}/verify", auth(s.handleVerifyBackup))

	mux.HandleFunc("GET /api/tasks", auth(s.handleListTasks))
	mux.HandleFunc("GET /api/tasks/{id}", auth(s.handleGetTask))
	mux.HandleFunc("GET /api/events", auth(s.handleEvents))

	mux.HandleFunc("GET /api/system/status", auth(s.handleSystemStatus))
	mux.HandleFunc("GET /api/system/drift", auth(s.handleListDrift))
	mux.HandleFunc("POST /api/system/drift/{id}/resolve", auth(s.handleResolveDrift))
	mux.HandleFunc("GET /api/audit", auth(s.handleAudit))
	mux.HandleFunc("GET /api/settings", auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", auth(s.handlePutSettings))

	// Everything else: the embedded single-page UI.
	mux.HandleFunc("/", s.handleUI)

	return s.withCommonHeaders(mux)
}

func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
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
	return site, true
}
