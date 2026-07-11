package rpc

import "github.com/slipstream-panel/slipstream/internal/state"

// CreateSiteParams provisions a complete site from desired state.
type CreateSiteParams struct {
	Site state.Site `json:"site"`
	// DBPassword is the generated password for the site's database user.
	DBPassword string `json:"db_password,omitempty"`
	// ConnectorToken authenticates the site's WordPress connector to the
	// panel's purge endpoint.
	ConnectorToken string `json:"connector_token,omitempty"`
	// WordPress bootstrap (ignored for other site types).
	AdminEmail    string `json:"admin_email,omitempty"`
	AdminUser     string `json:"admin_user,omitempty"`
	AdminPassword string `json:"admin_password,omitempty"`
	SiteTitle     string `json:"site_title,omitempty"`
}

// ManagedFile is one panel-rendered file and its content hash, recorded by
// the API for drift detection.
type ManagedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// CreateSiteResult reports what was provisioned.
type CreateSiteResult struct {
	SystemUser   string        `json:"system_user"`
	RootPath     string        `json:"root_path"`
	DatabaseName string        `json:"database_name,omitempty"`
	DatabaseUser string        `json:"database_user,omitempty"`
	Files        []ManagedFile `json:"files"`
}

// ApplyResult reports re-rendered managed files.
type ApplyResult struct {
	Files []ManagedFile `json:"files"`
}

// DriftParams asks the agent to compare managed files against recorded hashes.
type DriftParams struct {
	Expected map[string]string `json:"expected"` // path → sha256
}

// SiteRef addresses an existing site.
type SiteRef struct {
	Site state.Site `json:"site"`
}

// CertificateParams requests issuance/renewal for a site's domains.
type CertificateParams struct {
	Site    state.Site `json:"site"`
	Email   string     `json:"email"`
	Staging bool       `json:"staging"` // ACME staging endpoint for tests
}

// DatabaseParams creates or drops a database + dedicated user.
type DatabaseParams struct {
	Name     string `json:"name"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	MaxConns int    `json:"max_conns,omitempty"`
}

// DeployParams stages an immutable release from a source directory the API
// has prepared (upload extraction, git checkout, …).
type DeployParams struct {
	Site      state.Site `json:"site"`
	SourceDir string     `json:"source_dir"`
	ReleaseID string     `json:"release_id"` // assigned by the API, e.g. 20260711-151844
}

// DeployResult reports the created release.
type DeployResult struct {
	ReleaseID string `json:"release_id"`
	Path      string `json:"path"`
	Checksum  string `json:"checksum"`
}

// ReleaseParams promotes or rolls back a release symlink.
type ReleaseParams struct {
	Site      state.Site `json:"site"`
	ReleaseID string     `json:"release_id"`
}

// StagingParams clones production into an isolated staging site.
type StagingParams struct {
	Production     state.Site `json:"production"`
	Staging        state.Site `json:"staging"`
	DBPassword     string     `json:"db_password"`
	ConnectorToken string     `json:"connector_token"`
}

type SyncStagingDBParams struct {
	Production state.Site `json:"production"`
	Staging    state.Site `json:"staging"`
	Tables     []string   `json:"tables"`
}

// BackupParams runs a Restic backup of a site.
type BackupParams struct {
	Site       state.Site `json:"site"`
	Repository string     `json:"repository"` // repository URL, e.g. s3:...
	Password   string     `json:"password"`   // repository encryption key
	Kind       string     `json:"kind"`       // files | database | full
}

// BackupResult reports the created snapshot.
type BackupResult struct {
	SnapshotID string `json:"snapshot_id"`
	SizeBytes  int64  `json:"size_bytes"`
}

// RestoreParams restores a snapshot, either in place or into a target
// directory (used by verified restore tests).
type RestoreParams struct {
	Site       state.Site `json:"site"`
	Repository string     `json:"repository"`
	Password   string     `json:"password"`
	SnapshotID string     `json:"snapshot_id"`
	TargetDir  string     `json:"target_dir,omitempty"` // empty = in place
	Mode       string     `json:"mode,omitempty"`       // full | files | database
}

// VerifyResult is the outcome of a verified restore test.
type VerifyResult struct {
	Passed        bool   `json:"passed"`
	RestoreMillis int64  `json:"restore_millis"`
	Detail        string `json:"detail,omitempty"`
}

// PurgeParams removes specific URLs from a site's page cache. An empty URL
// list purges the whole site cache (used sparingly).
type PurgeParams struct {
	Site state.Site `json:"site"`
	URLs []string   `json:"urls"`
}

// PurgeResult reports how many cache entries were removed.
type PurgeResult struct {
	Removed int `json:"removed"`
}

// DriftResult lists managed files whose content changed outside the panel.
type DriftResult struct {
	Drifted []DriftedFile `json:"drifted"`
}

// DriftedFile is one managed file that no longer matches its recorded hash.
type DriftedFile struct {
	Path         string `json:"path"`
	ExpectedHash string `json:"expected_hash"`
	ActualHash   string `json:"actual_hash"`
}

// RestoreManagedParams re-renders one managed file from desired state.
type RestoreManagedParams struct {
	Path string     `json:"path"`
	Site state.Site `json:"site"`
}

// SystemStatusResult is the capacity-pressure snapshot for the dashboard.
type SystemStatusResult struct {
	CPUCount        int     `json:"cpu_count"`
	Load1           float64 `json:"load1"`
	MemTotalMB      int64   `json:"mem_total_mb"`
	MemAvailableMB  int64   `json:"mem_available_mb"`
	DiskTotalMB     int64   `json:"disk_total_mb"`
	DiskFreeMB      int64   `json:"disk_free_mb"`
	NginxRunning    bool    `json:"nginx_running"`
	MariaDBRunning  bool    `json:"mariadb_running"`
	RedisRunning    bool    `json:"redis_running"`
	AgentVersion    string  `json:"agent_version"`
	UptimeSeconds   int64   `json:"uptime_seconds"`
	CPUHeadroomPct  int     `json:"cpu_headroom_pct"`
	MemHeadroomPct  int     `json:"mem_headroom_pct"`
	DiskHeadroomPct int     `json:"disk_headroom_pct"`
}
