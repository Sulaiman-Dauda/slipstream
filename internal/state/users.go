package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateUser inserts a panel administrator.
func (s *Store) CreateUser(email, passwordHash, role string) (User, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO users (email, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		email, passwordHash, role, ts)
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Email: email, PasswordHash: passwordHash, Role: role, CreatedAt: parseTime(ts)}, nil
}

// GetUserByEmail fetches a user for login.
func (s *Store) GetUserByEmail(email string) (User, error) {
	var u User
	var created string
	err := s.db.QueryRow(`SELECT id, email, password_hash, role, created_at FROM users WHERE email=?`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}

// CountUsers reports how many users exist (0 means setup is incomplete).
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateSession stores a session token (hashed) for a user.
func (s *Store) CreateSession(token string, userID int64, ttl time.Duration) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, time.Now().UTC().Add(ttl).Format(time.RFC3339Nano), now())
	return err
}

// UserForSession resolves a session token to its user, if valid and unexpired.
func (s *Store) UserForSession(token string) (User, error) {
	var u User
	var created, expires string
	err := s.db.QueryRow(`
		SELECT u.id, u.email, u.password_hash, u.role, u.created_at, se.expires_at
		FROM sessions se JOIN users u ON u.id = se.user_id
		WHERE se.token_hash = ?`, hashToken(token)).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if parseTime(expires).Before(time.Now().UTC()) {
		s.DeleteSession(token)
		return User{}, ErrNotFound
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
	return err
}

// CreateSetupToken stores the one-time installer setup token.
func (s *Store) CreateSetupToken(token string, ttl time.Duration) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO setup_tokens (token_hash, expires_at, used) VALUES (?, ?, 0)`,
		hashToken(token), time.Now().UTC().Add(ttl).Format(time.RFC3339Nano))
	return err
}

// ConsumeSetupToken validates and burns a setup token. It returns ErrNotFound
// for unknown, expired, or already-used tokens.
func (s *Store) ConsumeSetupToken(token string) error {
	var expires string
	var used int
	err := s.db.QueryRow(`SELECT expires_at, used FROM setup_tokens WHERE token_hash=?`, hashToken(token)).
		Scan(&expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if used != 0 || parseTime(expires).Before(time.Now().UTC()) {
		return ErrNotFound
	}
	_, err = s.db.Exec(`UPDATE setup_tokens SET used=1 WHERE token_hash=?`, hashToken(token))
	return err
}
