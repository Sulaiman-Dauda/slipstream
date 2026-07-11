package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// CreateSite inserts a new site and returns it with its assigned ID.
func (s *Store) CreateSite(site Site) (Site, error) {
	aliases, err := json.Marshal(site.Aliases)
	if err != nil {
		return Site{}, err
	}
	cfg, err := json.Marshal(site.Config)
	if err != nil {
		return Site{}, err
	}
	ts := now()
	res, err := s.db.Exec(`
		INSERT INTO sites (domain, aliases, type, profile, engine, php_version, system_user, root_path, status, staging_of, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		site.Domain, string(aliases), string(site.Type), string(site.Profile), string(site.Engine),
		site.PHPVersion, site.SystemUser, site.RootPath, string(site.Status), site.StagingOf, string(cfg), ts, ts)
	if err != nil {
		return Site{}, fmt.Errorf("create site: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Site{}, err
	}
	return s.GetSite(id)
}

// UpdateSite persists changed fields of an existing site.
func (s *Store) UpdateSite(site Site) error {
	aliases, err := json.Marshal(site.Aliases)
	if err != nil {
		return err
	}
	cfg, err := json.Marshal(site.Config)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`
		UPDATE sites SET domain=?, aliases=?, type=?, profile=?, engine=?, php_version=?, system_user=?, root_path=?, status=?, staging_of=?, config=?, updated_at=?
		WHERE id=?`,
		site.Domain, string(aliases), string(site.Type), string(site.Profile), string(site.Engine),
		site.PHPVersion, site.SystemUser, site.RootPath, string(site.Status), site.StagingOf, string(cfg), now(), site.ID)
	if err != nil {
		return fmt.Errorf("update site: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSite removes a site record (cascades to deployments and backups).
func (s *Store) DeleteSite(id int64) error {
	res, err := s.db.Exec(`DELETE FROM sites WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const siteCols = `id, domain, aliases, type, profile, engine, php_version, system_user, root_path, status, staging_of, config, created_at, updated_at`

func scanSite(row interface{ Scan(...any) error }) (Site, error) {
	var (
		site                       Site
		aliases, cfg, created, upd string
		typ, profile, engine, st   string
	)
	err := row.Scan(&site.ID, &site.Domain, &aliases, &typ, &profile, &engine, &site.PHPVersion,
		&site.SystemUser, &site.RootPath, &st, &site.StagingOf, &cfg, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, ErrNotFound
	}
	if err != nil {
		return Site{}, err
	}
	site.Type, site.Profile, site.Engine, site.Status = SiteType(typ), Profile(profile), Engine(engine), SiteStatus(st)
	if err := json.Unmarshal([]byte(aliases), &site.Aliases); err != nil {
		return Site{}, fmt.Errorf("site %d aliases: %w", site.ID, err)
	}
	if err := json.Unmarshal([]byte(cfg), &site.Config); err != nil {
		return Site{}, fmt.Errorf("site %d config: %w", site.ID, err)
	}
	site.CreatedAt, site.UpdatedAt = parseTime(created), parseTime(upd)
	return site, nil
}

// GetSite fetches one site by ID.
func (s *Store) GetSite(id int64) (Site, error) {
	return scanSite(s.db.QueryRow(`SELECT `+siteCols+` FROM sites WHERE id=?`, id))
}

// GetSiteByDomain fetches one site by its primary domain.
func (s *Store) GetSiteByDomain(domain string) (Site, error) {
	return scanSite(s.db.QueryRow(`SELECT `+siteCols+` FROM sites WHERE domain=?`, domain))
}

// ListSites returns all sites ordered by domain.
func (s *Store) ListSites() ([]Site, error) {
	rows, err := s.db.Query(`SELECT ` + siteCols + ` FROM sites ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sites []Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

// StagingSiteFor returns the staging site of a production site, if any.
func (s *Store) StagingSiteFor(productionID int64) (Site, error) {
	return scanSite(s.db.QueryRow(`SELECT `+siteCols+` FROM sites WHERE staging_of=?`, productionID))
}
