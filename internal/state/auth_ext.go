package state

import (
	"database/sql"
	"errors"
	"time"
)

// --- TOTP / 2FA ---

// SetUserTOTPSecret stores a pending TOTP secret (not yet enabled).
func (s *Store) SetUserTOTPSecret(userID int64, secret string) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret=?, totp_enabled=0 WHERE id=?`, secret, userID)
	return err
}

// EnableUserTOTP marks 2FA active after the first code is verified.
func (s *Store) EnableUserTOTP(userID int64) error {
	_, err := s.db.Exec(`UPDATE users SET totp_enabled=1 WHERE id=?`, userID)
	return err
}

// DisableUserTOTP turns 2FA off and clears the secret.
func (s *Store) DisableUserTOTP(userID int64) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret='', totp_enabled=0 WHERE id=?`, userID)
	return err
}

// UserTOTP returns the stored secret and whether 2FA is enabled.
func (s *Store) UserTOTP(userID int64) (secret string, enabled bool, err error) {
	var en int
	err = s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id=?`, userID).Scan(&secret, &en)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	return secret, en == 1, err
}

// UpdatePassword replaces a user's password hash and invalidates all their
// existing sessions.
func (s *Store) UpdatePassword(userID int64, newHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET password_hash=? WHERE id=?`, newHash, userID); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- User management ---

// ListUsers returns all panel users (without password hashes).
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, email, role, created_at, totp_enabled FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created string
		var totp int
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &created, &totp); err != nil {
			return nil, err
		}
		u.CreatedAt = parseTime(created)
		u.TOTPEnabled = totp == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user (and cascades their sessions).
func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSessionsForUser returns active (unexpired) sessions for display.
func (s *Store) ListSessionsForUser(userID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT token_hash, created_at, expires_at FROM sessions WHERE user_id=? AND expires_at > ? ORDER BY created_at DESC`,
		userID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var se Session
		var created, expires string
		if err := rows.Scan(&se.TokenHash, &created, &expires); err != nil {
			return nil, err
		}
		se.CreatedAt = parseTime(created)
		se.ExpiresAt = parseTime(expires)
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteSessionByHash revokes a session by its stored hash prefix.
func (s *Store) DeleteSessionByHash(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hash)
	return err
}

// --- Schedule bookkeeping ---

// LastScheduleRun returns when a scheduler key last ran (zero if never).
func (s *Store) LastScheduleRun(key string) (time.Time, error) {
	var v string
	err := s.db.QueryRow(`SELECT last_run FROM schedule_state WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return parseTime(v), nil
}

// MarkScheduleRun records that a scheduler key ran now.
func (s *Store) MarkScheduleRun(key string) error {
	_, err := s.db.Exec(`INSERT INTO schedule_state (key, last_run) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET last_run=excluded.last_run`, key, now())
	return err
}
