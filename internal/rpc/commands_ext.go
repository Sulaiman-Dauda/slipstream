package rpc

import "github.com/slipstream-panel/slipstream/internal/state"

// --- Services & logs ---

// ServiceParams names a managed system service.
type ServiceParams struct {
	Name string `json:"name"` // nginx | php-fpm | mariadb | redis
}

// ServiceInfo is the status of one managed service.
type ServiceInfo struct {
	Name    string `json:"name"`
	Unit    string `json:"unit"`
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail,omitempty"`
}

// ServiceStatusResult lists all managed services.
type ServiceStatusResult struct {
	Services []ServiceInfo `json:"services"`
}

// TailLogParams reads the tail of a known log.
type TailLogParams struct {
	// Source is a logical name resolved by the agent to a safe path:
	//   nginx-access | nginx-error | php-error | mariadb | agent | api
	//   site:<domain>:access | site:<domain>:error | site:<domain>:php
	Source string `json:"source"`
	Site   string `json:"site,omitempty"`
	Lines  int    `json:"lines"`
}

// TailLogResult is the requested log content.
type TailLogResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// --- Cron ---

// CrontabParams replaces a site user's crontab with rendered content.
type CrontabParams struct {
	SystemUser string `json:"system_user"`
	Content    string `json:"content"`
}

type RunCronParams struct {
	SystemUser string `json:"system_user"`
	Command    string `json:"command"`
}

type RunCronResult struct {
	Status string `json:"status"`
	Output string `json:"output"`
}

// --- Firewall ---

// FirewallRuleParams opens or closes a port via UFW.
type FirewallRuleParams struct {
	Action string `json:"action"` // allow | deny | delete
	Port   int    `json:"port"`
	Proto  string `json:"proto"` // tcp | udp
	From   string `json:"from,omitempty"`
}

// FirewallStatusResult is the parsed UFW status.
type FirewallStatusResult struct {
	Enabled bool     `json:"enabled"`
	Rules   []string `json:"rules"`
}

// --- Database tools ---

// DBQueryParams runs SQL against a site's database as the panel's admin
// connection. The API decides which statements are permitted.
type DBQueryParams struct {
	Database string `json:"database"`
	SQL      string `json:"sql"`
}

// DBQueryResult returns tabular output.
type DBQueryResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Message string     `json:"message,omitempty"`
}

// DBExportParams dumps a database to a downloadable file under the site.
type DBExportParams struct {
	Site     state.Site `json:"site"`
	Database string     `json:"database"`
}

// DBExportResult reports the produced file.
type DBExportResult struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// DBImportParams imports a SQL file already uploaded inside the site's
// jailed root. Large dumps therefore stream locally instead of crossing RPC.
type DBImportParams struct {
	Site     state.Site `json:"site"`
	Database string     `json:"database"`
	RelPath  string     `json:"rel_path"`
}

type DBImportResult struct {
	Imported  string `json:"imported"`
	SizeBytes int64  `json:"size_bytes"`
}

type MigrationParams struct {
	Site      state.Site `json:"site"`
	Archive   string     `json:"archive"`
	SQL       string     `json:"sql,omitempty"`
	OldDomain string     `json:"old_domain,omitempty"`
	ReleaseID string     `json:"release_id"`
}

type MigrationResult struct {
	ReleaseID string `json:"release_id"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Skipped   int    `json:"skipped,omitempty"`
}

// AdminerParams drops a time-limited Adminer instance into a site.
type AdminerParams struct {
	Site       state.Site `json:"site"`
	Token      string     `json:"token"`
	DBName     string     `json:"db_name"`
	DBUser     string     `json:"db_user"`
	DBPassword string     `json:"db_password"`
	ExpiryUnix int64      `json:"expiry_unix"`
}

// AdminerResult is the launch URL.
type AdminerResult struct {
	URL string `json:"url"`
}

// --- Files ---

// ListFilesParams lists a directory relative to a site root.
type ListFilesParams struct {
	Site    state.Site `json:"site"`
	RelPath string     `json:"rel_path"`
}

// FileEntry is one directory entry.
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
}

// ListFilesResult is a directory listing.
type ListFilesResult struct {
	RelPath string      `json:"rel_path"`
	Entries []FileEntry `json:"entries"`
}

// ReadFileParams reads a text file relative to a site root.
type ReadFileParams struct {
	Site    state.Site `json:"site"`
	RelPath string     `json:"rel_path"`
}

// ReadFileResult returns file content (capped).
type ReadFileResult struct {
	RelPath   string `json:"rel_path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// WriteFileParams writes a text file relative to a site root.
type WriteFileParams struct {
	Site    state.Site `json:"site"`
	RelPath string     `json:"rel_path"`
	Content string     `json:"content"`
}

// TransferFileParams uploads or downloads bounded binary data.
type TransferFileParams struct {
	Site    state.Site `json:"site"`
	RelPath string     `json:"rel_path"`
	Data    []byte     `json:"data,omitempty"`
	Upload  bool       `json:"upload"`
}

type TransferFileResult struct {
	Name string `json:"name"`
	Data []byte `json:"data,omitempty"`
	Size int64  `json:"size"`
}

// ManageFileParams performs a structural operation inside the site jail.
type ManageFileParams struct {
	Site      state.Site `json:"site"`
	Operation string     `json:"operation"` // mkdir | rename | delete
	RelPath   string     `json:"rel_path"`
	DestPath  string     `json:"dest_path,omitempty"`
}

// SFTPParams sets a site user's SFTP password and enables SFTP access.
type SFTPParams struct {
	Site     state.Site `json:"site"`
	Password string     `json:"password"`
	Enable   bool       `json:"enable"`
}

type SSHKeyParams struct {
	Site        state.Site `json:"site"`
	Action      string     `json:"action"` // list | add | delete
	PublicKey   string     `json:"public_key,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
}

type SSHKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label,omitempty"`
}

type SSHKeysResult struct {
	Keys []SSHKey `json:"keys"`
}

// --- WordPress toolkit ---

// WPParams addresses a WordPress site for wp-cli operations.
type WPParams struct {
	Site state.Site `json:"site"`
	// What selects the update target: core | plugins | themes | all
	What string `json:"what,omitempty"`
	// Enable toggles a feature (object cache).
	Enable bool `json:"enable,omitempty"`
}

// WPLoginResult is a one-time magic login URL.
type WPLoginResult struct {
	URL string `json:"url"`
}

// CacheStatsResult reports object-cache effectiveness.
type CacheStatsResult struct {
	Backend    string `json:"backend"` // apcu | redis | none
	Hits       int64  `json:"hits"`
	Misses     int64  `json:"misses"`
	Entries    int64  `json:"entries"`
	MemUsed    int64  `json:"mem_used"`
	MemTotal   int64  `json:"mem_total"`
	HitRatePct int    `json:"hit_rate_pct"`
}

// HitsPlusMisses is the total sampled operations.
func (c CacheStatsResult) HitsPlusMisses() int64 { return c.Hits + c.Misses }

// WarmParams pre-fills a site's full-page cache from its sitemap.
type WarmParams struct {
	Site state.Site `json:"site"`
	// MaxURLs caps how many URLs to warm (0 = default).
	MaxURLs int `json:"max_urls"`
}

// WarmResult reports the warming outcome.
type WarmResult struct {
	Warmed int `json:"warmed"`
	Cached int `json:"cached"` // how many came back as cache HIT after warming
}

// WPPlugin is one installed plugin/theme.
type WPPlugin struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version"`
	Update  string `json:"update"`
}

// WPPluginsResult lists plugins and available updates.
type WPPluginsResult struct {
	Plugins []WPPlugin `json:"plugins"`
	Themes  []WPPlugin `json:"themes"`
}

// --- Panel cert & self-update ---

// PanelCertParams issues a Let's Encrypt certificate for the panel's own
// domain and installs it as the panel TLS certificate.
type PanelCertParams struct {
	Domain string `json:"domain"`
	Email  string `json:"email"`
	Port   int    `json:"port"`
}

// SelfUpdateParams points the agent at signed replacement binaries.
type SelfUpdateParams struct {
	BaseURL string `json:"base_url"`
	Version string `json:"version"`
}

// SelfUpdateResult reports the outcome.
type SelfUpdateResult struct {
	UpdatedTo string `json:"updated_to"`
	Restarted bool   `json:"restarted"`
}
