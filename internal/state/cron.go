package state

import (
	"database/sql"
	"errors"
	"time"
)

// CreateCronJob inserts a scheduled job for a site.
func (s *Store) CreateCronJob(j CronJob) (CronJob, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO cron_jobs (site_id, schedule, command, description, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		j.SiteID, j.Schedule, j.Command, j.Description, boolToInt(j.Enabled), ts)
	if err != nil {
		return CronJob{}, err
	}
	j.ID, _ = res.LastInsertId()
	j.CreatedAt = parseTime(ts)
	return j, nil
}

// ListCronJobs returns a site's cron jobs.
func (s *Store) ListCronJobs(siteID int64) ([]CronJob, error) {
	rows, err := s.db.Query(`SELECT id, site_id, schedule, command, description, enabled, last_run, last_status, created_at
		FROM cron_jobs WHERE site_id=? ORDER BY id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJob
	for rows.Next() {
		j, err := scanCron(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetCronJob fetches one cron job.
func (s *Store) GetCronJob(id int64) (CronJob, error) {
	return scanCron(s.db.QueryRow(`SELECT id, site_id, schedule, command, description, enabled, last_run, last_status, created_at
		FROM cron_jobs WHERE id=?`, id))
}

// DeleteCronJob removes a cron job.
func (s *Store) DeleteCronJob(id int64) error {
	res, err := s.db.Exec(`DELETE FROM cron_jobs WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateCronRun(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE cron_jobs SET last_run=?, last_status=? WHERE id=?`, now(), status, id)
	return err
}

func scanCron(row interface{ Scan(...any) error }) (CronJob, error) {
	var j CronJob
	var enabled int
	var lastRun sql.NullString
	var created string
	err := row.Scan(&j.ID, &j.SiteID, &j.Schedule, &j.Command, &j.Description, &enabled, &lastRun, &j.LastStatus, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CronJob{}, ErrNotFound
	}
	if err != nil {
		return CronJob{}, err
	}
	j.Enabled = enabled == 1
	j.LastRun = parseTimePtr(lastRun)
	j.CreatedAt = parseTime(created)
	return j, nil
}

// --- Adminer sessions ---

// CreateAdminerSession stores a time-limited database-tool token.
func (s *Store) CreateAdminerSession(token string, siteID int64, ttl time.Duration) error {
	_, err := s.db.Exec(`INSERT INTO adminer_sessions (token_hash, site_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), siteID, time.Now().UTC().Add(ttl).Format(time.RFC3339Nano), now())
	return err
}

// PurgeExpiredAdminerSessions clears stale database-tool tokens.
func (s *Store) PurgeExpiredAdminerSessions() error {
	_, err := s.db.Exec(`DELETE FROM adminer_sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
