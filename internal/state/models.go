// Package state is Slipstream's single source of truth: the desired
// configuration of every site and server component, stored in SQLite.
// The agent renders real system configuration from these records; the
// records are never derived from the system.
package state

import "time"

// SiteType identifies the provisioning recipe for a site.
type SiteType string

const (
	SiteWordPress   SiteType = "wordpress"
	SiteWooCommerce SiteType = "woocommerce"
	SiteStatic      SiteType = "static"
	SitePHP         SiteType = "php"
	SiteLaravel     SiteType = "laravel"
	SiteProxy       SiteType = "proxy"
)

// Profile selects a Velocity Engine cache/tuning policy.
type Profile string

const (
	ProfileBalanced Profile = "balanced"
	ProfileCommerce Profile = "commerce"
	ProfileMaximum  Profile = "maximum"
)

// Engine selects the rendered web-serving stack.
type Engine string

const (
	EngineNginx Engine = "nginx" // default: Nginx + PHP-FPM
)

// SiteStatus is the lifecycle state of a site.
type SiteStatus string

const (
	SiteProvisioning SiteStatus = "provisioning"
	SiteActive       SiteStatus = "active"
	SiteError        SiteStatus = "error"
	SiteDeleting     SiteStatus = "deleting"
)

// DatabaseConfig describes the database a site uses. External databases
// (managed or remote) are first-class so sites can outgrow the local server
// without being recreated.
type DatabaseConfig struct {
	Enabled  bool   `json:"enabled"`
	External bool   `json:"external"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Name     string `json:"name"`
	User     string `json:"user"`
	TLS      bool   `json:"tls"`
}

// Resources are per-site limits enforced through systemd/cgroup v2.
type Resources struct {
	CPUWeight     int `json:"cpu_weight"`
	MemoryHighMB  int `json:"memory_high_mb"`
	MemoryMaxMB   int `json:"memory_max_mb"`
	PHPWorkers    int `json:"php_workers"` // 0 = auto-size
	DBConnections int `json:"db_connections"`
}

// BackupPolicy configures the Restic schedule for a site.
type BackupPolicy struct {
	Enabled       bool   `json:"enabled"`
	Schedule      string `json:"schedule"` // hourly | daily | weekly
	RetentionDays int    `json:"retention_days"`
	Repository    string `json:"repository"` // named repository from settings
}

// PHPSettings are curated per-site php.ini overrides. Zero values mean
// "use the sensible default the pool renderer computes".
type PHPSettings struct {
	MemoryLimitMB       int `json:"memory_limit_mb"`
	UploadMaxMB         int `json:"upload_max_mb"`
	MaxExecutionSeconds int `json:"max_execution_seconds"`
}

// SiteConfig is the declarative site description stored as JSON.
type SiteConfig struct {
	CacheEnabled  bool           `json:"cache_enabled"`
	CacheTTLSec   int            `json:"cache_ttl_sec"` // 0 = profile default
	ObjectCache   bool           `json:"object_cache"`  // per-site Redis namespace
	Database      DatabaseConfig `json:"database"`
	Resources     Resources      `json:"resources"`
	Backups       BackupPolicy   `json:"backups"`
	PHP           PHPSettings    `json:"php"`
	SFTPEnabled   bool           `json:"sftp_enabled"`
	ProxyUpstream string         `json:"proxy_upstream,omitempty"` // for SiteProxy
	PublicRoot    string         `json:"public_root,omitempty"`    // docroot relative to release, e.g. "public"
}

// Site is one managed website. A staging site references its production
// site via StagingOf.
type Site struct {
	ID         int64      `json:"id"`
	Domain     string     `json:"domain"`
	Aliases    []string   `json:"aliases"`
	Type       SiteType   `json:"type"`
	Profile    Profile    `json:"profile"`
	Engine     Engine     `json:"engine"`
	PHPVersion string     `json:"php_version"`
	SystemUser string     `json:"system_user"`
	RootPath   string     `json:"root_path"`
	Status     SiteStatus `json:"status"`
	StagingOf  int64      `json:"staging_of,omitempty"`
	Config     SiteConfig `json:"config"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TaskStatus is the lifecycle of a background task.
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
)

// Task is a tracked background operation (provisioning, deploy, backup, …).
type Task struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	SiteID     int64      `json:"site_id,omitempty"`
	Status     TaskStatus `json:"status"`
	Progress   int        `json:"progress"` // 0–100
	Message    string     `json:"message"`
	Log        string     `json:"log"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// DeploymentStatus tracks an immutable release through Performance Guard.
type DeploymentStatus string

const (
	DeployCreated    DeploymentStatus = "created"
	DeployGuarding   DeploymentStatus = "guarding"
	DeployBlocked    DeploymentStatus = "blocked"
	DeployPromoted   DeploymentStatus = "promoted"
	DeployRolledBack DeploymentStatus = "rolled_back"
)

// Deployment is one immutable release of a site's code.
type Deployment struct {
	ID         int64            `json:"id"`
	SiteID     int64            `json:"site_id"`
	ReleaseID  string           `json:"release_id"` // e.g. 20260711-151844
	Path       string           `json:"path"`
	Checksum   string           `json:"checksum"`
	Status     DeploymentStatus `json:"status"`
	GuardJSON  string           `json:"guard_json,omitempty"` // serialized guard.Report
	CreatedAt  time.Time        `json:"created_at"`
	PromotedAt *time.Time       `json:"promoted_at,omitempty"`
}

// BackupVerify is the outcome of the most recent restore test.
type BackupVerify string

const (
	VerifyPending BackupVerify = "pending"
	VerifyPassed  BackupVerify = "passed"
	VerifyFailed  BackupVerify = "failed"
)

// Backup records one Restic snapshot and its verification state.
type Backup struct {
	ID           int64        `json:"id"`
	SiteID       int64        `json:"site_id"`
	SnapshotID   string       `json:"snapshot_id"`
	Repository   string       `json:"repository"`
	SizeBytes    int64        `json:"size_bytes"`
	Kind         string       `json:"kind"` // files | database | full
	VerifyStatus BackupVerify `json:"verify_status"`
	VerifiedAt   *time.Time   `json:"verified_at,omitempty"`
	RestoreEstMS int64        `json:"restore_estimate_ms,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

// User is a panel administrator account.
type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"` // admin | operator | readonly
	SiteIDs      []int64   `json:"site_ids,omitempty"`
	TOTPEnabled  bool      `json:"totp_enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// Session is an active panel login session.
type Session struct {
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CronJob is a scheduled command that runs as a site's Unix user.
type CronJob struct {
	ID          int64      `json:"id"`
	SiteID      int64      `json:"site_id"`
	Schedule    string     `json:"schedule"` // 5-field cron
	Command     string     `json:"command"`
	Description string     `json:"description"`
	Enabled     bool       `json:"enabled"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	LastStatus  string     `json:"last_status"`
	CreatedAt   time.Time  `json:"created_at"`
}

// MetricSample is a deliberately small capacity snapshot. The store retains
// only one day, avoiding a separate time-series service and unbounded data.
type MetricSample struct {
	CPUHeadroomPct  int       `json:"cpu_headroom_pct"`
	MemHeadroomPct  int       `json:"mem_headroom_pct"`
	DiskHeadroomPct int       `json:"disk_headroom_pct"`
	Load1           float64   `json:"load1"`
	SampledAt       time.Time `json:"sampled_at"`
}

// AuditEvent is one immutable audit-log entry.
type AuditEvent struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Subject   string    `json:"subject"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// DriftStatus is the reconciliation state of detected drift.
type DriftStatus string

const (
	DriftOpen     DriftStatus = "open"
	DriftRestored DriftStatus = "restored"
	DriftAccepted DriftStatus = "accepted"
)

// DriftEvent records managed configuration changed outside the panel.
type DriftEvent struct {
	ID           int64       `json:"id"`
	Path         string      `json:"path"`
	ExpectedHash string      `json:"expected_hash"`
	ActualHash   string      `json:"actual_hash"`
	Diff         string      `json:"diff,omitempty"`
	Status       DriftStatus `json:"status"`
	DetectedAt   time.Time   `json:"detected_at"`
	ResolvedAt   *time.Time  `json:"resolved_at,omitempty"`
}
