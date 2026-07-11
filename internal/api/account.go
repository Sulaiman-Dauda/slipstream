package api

import (
	"net/http"
	"net/mail"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// --- Password change ---

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req changePasswordRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !verifyPassword(req.CurrentPassword, user.PasswordHash) {
		respondErr(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	if len(req.NewPassword) < 12 {
		respondErr(w, http.StatusBadRequest, "new password must be at least 12 characters")
		return
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.UpdatePassword(user.ID, hash); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(user.Email, "password.change", "self", "")
	// Password change invalidates all sessions including this one; start a
	// fresh session so the user stays logged in on this device.
	s.startSession(w, user)
	respond(w, http.StatusOK, map[string]string{"status": "password changed"})
}

// --- 2FA enrollment ---

// handleTOTPBegin generates a new secret and returns the provisioning URI
// plus a QR-code data URI. 2FA is not active until a code is confirmed.
func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	secret := newTOTPSecret()
	if err := s.Store.SetUserTOTPSecret(user.ID, secret); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	uri := totpURI(secret, user.Email, "Slipstream")
	png, err := qrcode.Encode(uri, qrcode.Medium, 256)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]string{
		"secret":  secret,
		"uri":     uri,
		"qr_data": "data:image/png;base64," + base64Std(png),
	})
}

type totpConfirmRequest struct {
	Code string `json:"code"`
}

// handleTOTPConfirm verifies the first code and activates 2FA.
func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req totpConfirmRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	secret, _, err := s.Store.UserTOTP(user.ID)
	if err != nil || secret == "" {
		respondErr(w, http.StatusBadRequest, "start enrollment first")
		return
	}
	if !verifyTOTP(secret, req.Code, time.Now()) {
		respondErr(w, http.StatusBadRequest, "code did not match — check your device clock")
		return
	}
	if err := s.Store.EnableUserTOTP(user.ID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(user.Email, "2fa.enabled", "self", "")
	respond(w, http.StatusOK, map[string]string{"status": "two-factor enabled"})
}

// handleTOTPDisable turns 2FA off (requires the current password).
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !verifyPassword(req.Password, user.PasswordHash) {
		respondErr(w, http.StatusForbidden, "password is incorrect")
		return
	}
	if err := s.Store.DisableUserTOTP(user.ID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(user.Email, "2fa.disabled", "self", "")
	respond(w, http.StatusOK, map[string]string{"status": "two-factor disabled"})
}

// --- User management ---

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []state.User{}
	}
	respond(w, http.StatusOK, users)
}

type createUserRequest struct {
	Email    string  `json:"email"`
	Password string  `json:"password"`
	Role     string  `json:"role"`
	SiteIDs  []int64 `json:"site_ids"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 12 {
		respondErr(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	role := req.Role
	if role != "admin" && role != "operator" && role != "readonly" {
		role = "admin"
	}
	if role == "operator" && len(req.SiteIDs) == 0 {
		respondErr(w, http.StatusBadRequest, "assign at least one site to an operator")
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := s.Store.CreateUser(strings.ToLower(req.Email), hash, role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			respondErr(w, http.StatusConflict, "a user with this email already exists")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if role == "operator" {
		if err := s.Store.SetUserSites(user.ID, req.SiteIDs); err != nil {
			s.Store.DeleteUser(user.ID)
			respondErr(w, http.StatusBadRequest, "invalid site assignment")
			return
		}
		user.SiteIDs = req.SiteIDs
	}
	s.Store.Audit(s.actor(r), "user.create", user.Email, role)
	respond(w, http.StatusCreated, user)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	me, _ := s.sessionUser(r)
	if me.ID == id {
		respondErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	users, _ := s.Store.ListUsers()
	adminCount := 0
	targetIsAdmin := false
	for _, user := range users {
		if user.Role == "admin" {
			adminCount++
		}
		if user.ID == id {
			targetIsAdmin = user.Role == "admin"
		}
	}
	if targetIsAdmin && adminCount <= 1 {
		respondErr(w, http.StatusBadRequest, "cannot delete the last administrator")
		return
	}
	if err := s.Store.DeleteUser(id); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "user.delete", "", "")
	respond(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleListSessions shows the current user's active sessions.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	sessions, err := s.Store.ListSessionsForUser(user.ID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Tag the caller's own session (by hash of their cookie) so the UI can
	// avoid offering to revoke it.
	current := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		current = sessionTokenHash(c.Value)
	}
	type sessionView struct {
		ID        string    `json:"id"` // first 12 chars of the hash
		Current   bool      `json:"current"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	out := make([]sessionView, 0, len(sessions))
	for _, se := range sessions {
		out = append(out, sessionView{
			ID: se.TokenHash[:12], Current: se.TokenHash == current,
			CreatedAt: se.CreatedAt, ExpiresAt: se.ExpiresAt,
		})
	}
	respond(w, http.StatusOK, out)
}

// handleRevokeSession revokes one of the user's sessions by hash prefix.
func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	user, err := s.sessionUser(r)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	prefix := r.PathValue("id")
	sessions, err := s.Store.ListSessionsForUser(user.ID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, se := range sessions {
		if strings.HasPrefix(se.TokenHash, prefix) {
			s.Store.DeleteSessionByHash(se.TokenHash)
			s.Store.Audit(user.Email, "session.revoke", "self", "")
			respond(w, http.StatusOK, map[string]string{"status": "revoked"})
			return
		}
	}
	respondErr(w, http.StatusNotFound, "session not found")
}
