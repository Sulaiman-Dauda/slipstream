package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/slipstream-panel/slipstream/internal/state"
)

const sessionCookie = "slipstream_session"
const sessionTTL = 24 * time.Hour

// dummyHash is a valid argon2id hash of a random value, used to equalize
// login timing for nonexistent accounts. Computed once at startup.
var dummyHash = func() string { h, _ := hashPassword(randomToken(16)); return h }()

// hashPassword produces an argon2id hash in a self-describing format.
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Token    string `json:"token,omitempty"`
	TOTP     string `json:"totp,omitempty"`
}

// handleBootstrap tells the UI whether initial setup is still pending.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.CountUsers()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"setup_complete": n > 0})
}

// handleSetup consumes the one-time installer token and creates the first
// administrator.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decode(r, &c); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if n, _ := s.Store.CountUsers(); n > 0 {
		respondErr(w, http.StatusConflict, "setup already complete")
		return
	}
	if err := s.Store.ConsumeSetupToken(c.Token); err != nil {
		respondErr(w, http.StatusForbidden, "invalid or expired setup token")
		return
	}
	if _, err := mail.ParseAddress(c.Email); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(c.Password) < 12 {
		respondErr(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	hash, err := hashPassword(c.Password)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := s.Store.CreateUser(strings.ToLower(c.Email), hash, "admin")
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(user.Email, "setup.complete", "panel", "")
	s.startSession(w, user)
	respond(w, http.StatusCreated, user)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := decode(r, &c); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	email := strings.ToLower(c.Email)

	// Rate limit by client IP + email to blunt credential stuffing.
	if !s.loginLimiter.allow(clientIP(r) + "|" + email) {
		respondErr(w, http.StatusTooManyRequests, "too many attempts — wait a minute and try again")
		return
	}

	user, err := s.Store.GetUserByEmail(email)
	if errors.Is(err, state.ErrNotFound) {
		// Run a dummy argon2 verify so a nonexistent account takes the same
		// time as a wrong password — no timing-based user enumeration.
		verifyPassword(c.Password, dummyHash)
		s.Store.Audit(c.Email, "login.failed", "panel", clientIP(r))
		respondErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err == nil && !verifyPassword(c.Password, user.PasswordHash) {
		s.Store.Audit(c.Email, "login.failed", "panel", clientIP(r))
		respondErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Second factor, if enrolled.
	if secret, enabled, _ := s.Store.UserTOTP(user.ID); enabled {
		if c.TOTP == "" {
			respond(w, http.StatusUnauthorized, map[string]any{"error": "two-factor code required", "totp_required": true})
			return
		}
		if !verifyTOTP(secret, c.TOTP, time.Now()) {
			s.Store.Audit(user.Email, "login.totp_failed", "panel", clientIP(r))
			respond(w, http.StatusUnauthorized, map[string]any{"error": "invalid two-factor code", "totp_required": true})
			return
		}
	}

	s.loginLimiter.reset(clientIP(r) + "|" + email)
	s.Store.Audit(user.Email, "login.success", "panel", clientIP(r))
	s.startSession(w, user)
	respond(w, http.StatusOK, user)
}

func (s *Server) startSession(w http.ResponseWriter, user state.User) {
	token := randomToken(32)
	s.Store.CreateSession(token, user.ID, sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   !s.InsecureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	respond(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := s.sessionUser(r)
	respond(w, http.StatusOK, user)
}

func (s *Server) sessionUser(r *http.Request) (state.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return state.User{}, err
	}
	return s.Store.UserForSession(c.Value)
}

// requireSession guards a handler behind a valid session.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.sessionUser(r); err != nil {
			respondErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

// requireAdmin guards state-changing endpoints so read-only accounts can
// view everything but change nothing (they may still manage their own
// account via the /api/account routes, which use requireSession).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := s.sessionUser(r)
		if err != nil {
			respondErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if user.Role != "admin" {
			respondErr(w, http.StatusForbidden, "this is a read-only account")
			return
		}
		next(w, r)
	}
}

// actor returns the audit identity for a request.
func (s *Server) actor(r *http.Request) string {
	if u, err := s.sessionUser(r); err == nil {
		return u.Email
	}
	return "unknown"
}
