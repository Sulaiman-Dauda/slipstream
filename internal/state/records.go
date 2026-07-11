package state

import (
	"database/sql"
	"errors"
)

// --- Deployments ---

// CreateDeployment records a new immutable release.
func (s *Store) CreateDeployment(d Deployment) (Deployment, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO deployments (site_id, release_id, path, checksum, status, guard_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.SiteID, d.ReleaseID, d.Path, d.Checksum, string(d.Status), d.GuardJSON, ts)
	if err != nil {
		return Deployment{}, err
	}
	d.ID, _ = res.LastInsertId()
	d.CreatedAt = parseTime(ts)
	return d, nil
}

// SetDeploymentStatus advances a deployment through the release lifecycle,
// optionally attaching a Performance Guard report.
func (s *Store) SetDeploymentStatus(id int64, status DeploymentStatus, guardJSON string) error {
	var promoted any
	if status == DeployPromoted {
		promoted = now()
	}
	res, err := s.db.Exec(`UPDATE deployments SET status=?, guard_json=CASE WHEN ?='' THEN guard_json ELSE ? END, promoted_at=COALESCE(?, promoted_at) WHERE id=?`,
		string(status), guardJSON, guardJSON, promoted, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const deployCols = `id, site_id, release_id, path, checksum, status, guard_json, created_at, promoted_at`

func scanDeployment(row interface{ Scan(...any) error }) (Deployment, error) {
	var d Deployment
	var status, created string
	var promoted sql.NullString
	err := row.Scan(&d.ID, &d.SiteID, &d.ReleaseID, &d.Path, &d.Checksum, &status, &d.GuardJSON, &created, &promoted)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	d.Status = DeploymentStatus(status)
	d.CreatedAt = parseTime(created)
	d.PromotedAt = parseTimePtr(promoted)
	return d, nil
}

// ListDeployments returns a site's releases, newest first.
func (s *Store) ListDeployments(siteID int64, limit int) ([]Deployment, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+deployCols+` FROM deployments WHERE site_id=? ORDER BY id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDeployment fetches one deployment.
func (s *Store) GetDeployment(id int64) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(`SELECT `+deployCols+` FROM deployments WHERE id=?`, id))
}

// --- Backups ---

// CreateBackup records a completed Restic snapshot.
func (s *Store) CreateBackup(b Backup) (Backup, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO backups (site_id, snapshot_id, repository, size_bytes, kind, verify_status, restore_est_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.SiteID, b.SnapshotID, b.Repository, b.SizeBytes, b.Kind, string(VerifyPending), b.RestoreEstMS, ts)
	if err != nil {
		return Backup{}, err
	}
	b.ID, _ = res.LastInsertId()
	b.VerifyStatus = VerifyPending
	b.CreatedAt = parseTime(ts)
	return b, nil
}

// SetBackupVerified records the outcome and duration of a restore test.
func (s *Store) SetBackupVerified(id int64, status BackupVerify, restoreMS int64) error {
	_, err := s.db.Exec(`UPDATE backups SET verify_status=?, verified_at=?, restore_est_ms=? WHERE id=?`,
		string(status), now(), restoreMS, id)
	return err
}

// GetBackup fetches one backup by ID.
func (s *Store) GetBackup(id int64) (Backup, error) {
	var b Backup
	var vs, created string
	var verified sql.NullString
	err := s.db.QueryRow(`SELECT id, site_id, snapshot_id, repository, size_bytes, kind, verify_status, verified_at, restore_est_ms, created_at
		FROM backups WHERE id=?`, id).
		Scan(&b.ID, &b.SiteID, &b.SnapshotID, &b.Repository, &b.SizeBytes, &b.Kind, &vs, &verified, &b.RestoreEstMS, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Backup{}, ErrNotFound
	}
	if err != nil {
		return Backup{}, err
	}
	b.VerifyStatus = BackupVerify(vs)
	b.VerifiedAt = parseTimePtr(verified)
	b.CreatedAt = parseTime(created)
	return b, nil
}

// ListBackups returns a site's backups, newest first.
func (s *Store) ListBackups(siteID int64, limit int) ([]Backup, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, site_id, snapshot_id, repository, size_bytes, kind, verify_status, verified_at, restore_est_ms, created_at
		FROM backups WHERE site_id=? ORDER BY id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		var b Backup
		var vs, created string
		var verified sql.NullString
		if err := rows.Scan(&b.ID, &b.SiteID, &b.SnapshotID, &b.Repository, &b.SizeBytes, &b.Kind, &vs, &verified, &b.RestoreEstMS, &created); err != nil {
			return nil, err
		}
		b.VerifyStatus = BackupVerify(vs)
		b.VerifiedAt = parseTimePtr(verified)
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// --- Audit log ---

// Audit appends an immutable audit event.
func (s *Store) Audit(actor, action, subject, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit_events (actor, action, subject, detail, created_at) VALUES (?, ?, ?, ?, ?)`,
		actor, action, subject, detail, now())
	return err
}

// ListAuditEvents returns recent audit events, newest first.
func (s *Store) ListAuditEvents(limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, actor, action, subject, detail, created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var created string
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Subject, &e.Detail, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Settings ---

// SetSetting stores a key/value setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetSetting fetches a setting, returning def when absent.
func (s *Store) GetSetting(key, def string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// --- Drift ---

// RecordManagedFile stores the hash of a panel-rendered file for drift checks.
func (s *Store) RecordManagedFile(path, sha string) error {
	_, err := s.db.Exec(`INSERT INTO managed_files (path, sha256, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET sha256=excluded.sha256, updated_at=excluded.updated_at`, path, sha, now())
	return err
}

// ManagedFiles returns all path→hash pairs the panel owns.
func (s *Store) ManagedFiles() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT path, sha256 FROM managed_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		out[p] = h
	}
	return out, rows.Err()
}

// RemoveManagedFile forgets a managed file (site deleted).
func (s *Store) RemoveManagedFile(path string) error {
	_, err := s.db.Exec(`DELETE FROM managed_files WHERE path=?`, path)
	return err
}

// CreateDriftEvent records configuration changed outside the panel.
func (s *Store) CreateDriftEvent(d DriftEvent) (DriftEvent, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO drift_events (path, expected_hash, actual_hash, diff, status, detected_at)
		VALUES (?, ?, ?, ?, ?, ?)`, d.Path, d.ExpectedHash, d.ActualHash, d.Diff, string(DriftOpen), ts)
	if err != nil {
		return DriftEvent{}, err
	}
	d.ID, _ = res.LastInsertId()
	d.Status = DriftOpen
	d.DetectedAt = parseTime(ts)
	return d, nil
}

// ResolveDriftEvent marks drift restored (panel config re-applied) or
// accepted (operator kept the manual change).
func (s *Store) ResolveDriftEvent(id int64, status DriftStatus) error {
	_, err := s.db.Exec(`UPDATE drift_events SET status=?, resolved_at=? WHERE id=?`, string(status), now(), id)
	return err
}

// ListDriftEvents returns open drift events, newest first.
func (s *Store) ListDriftEvents(includeResolved bool) ([]DriftEvent, error) {
	q := `SELECT id, path, expected_hash, actual_hash, diff, status, detected_at, resolved_at FROM drift_events`
	if !includeResolved {
		q += ` WHERE status='open'`
	}
	q += ` ORDER BY id DESC LIMIT 200`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DriftEvent
	for rows.Next() {
		var d DriftEvent
		var status, detected string
		var resolved sql.NullString
		if err := rows.Scan(&d.ID, &d.Path, &d.ExpectedHash, &d.ActualHash, &d.Diff, &status, &detected, &resolved); err != nil {
			return nil, err
		}
		d.Status = DriftStatus(status)
		d.DetectedAt = parseTime(detected)
		d.ResolvedAt = parseTimePtr(resolved)
		out = append(out, d)
	}
	return out, rows.Err()
}
