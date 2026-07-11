package state

// migrations are applied in order; index+1 is the schema version.
// Never edit an existing entry — append a new one.
var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	email TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'admin',
	created_at TEXT NOT NULL
);

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE setup_tokens (
	token_hash TEXT PRIMARY KEY,
	expires_at TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE sites (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	domain TEXT NOT NULL UNIQUE,
	aliases TEXT NOT NULL DEFAULT '[]',
	type TEXT NOT NULL,
	profile TEXT NOT NULL,
	engine TEXT NOT NULL DEFAULT 'nginx',
	php_version TEXT NOT NULL DEFAULT '',
	system_user TEXT NOT NULL,
	root_path TEXT NOT NULL,
	status TEXT NOT NULL,
	staging_of INTEGER NOT NULL DEFAULT 0,
	config TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX idx_sites_staging_of ON sites(staging_of);

CREATE TABLE tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	site_id INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	progress INTEGER NOT NULL DEFAULT 0,
	message TEXT NOT NULL DEFAULT '',
	log TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT
);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_site ON tasks(site_id);

CREATE TABLE deployments (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	release_id TEXT NOT NULL,
	path TEXT NOT NULL,
	checksum TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	guard_json TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	promoted_at TEXT,
	UNIQUE(site_id, release_id)
);

CREATE TABLE backups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	snapshot_id TEXT NOT NULL,
	repository TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL,
	verify_status TEXT NOT NULL DEFAULT 'pending',
	verified_at TEXT,
	restore_est_ms INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX idx_backups_site ON backups(site_id);

CREATE TABLE audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	subject TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);

CREATE TABLE drift_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	path TEXT NOT NULL,
	expected_hash TEXT NOT NULL,
	actual_hash TEXT NOT NULL,
	diff TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'open',
	detected_at TEXT NOT NULL,
	resolved_at TEXT
);

CREATE TABLE managed_files (
	path TEXT PRIMARY KEY,
	sha256 TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`,
	// 2: auth hardening, cron, adminer, php settings, scheduler bookkeeping
	`
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;

CREATE TABLE cron_jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	schedule TEXT NOT NULL,
	command TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	last_run TEXT,
	last_status TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX idx_cron_site ON cron_jobs(site_id);

CREATE TABLE adminer_sessions (
	token_hash TEXT PRIMARY KEY,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL
);

-- Per-site scheduler bookkeeping: when we last ran automatic jobs.
CREATE TABLE schedule_state (
	key TEXT PRIMARY KEY,
	last_run TEXT NOT NULL
);
`,
	// 3: site-scoped operator access
	`
CREATE TABLE user_sites (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, site_id)
);
CREATE INDEX idx_user_sites_site ON user_sites(site_id);
`,
	// 4: bounded server-capacity history (24 hours at five-minute intervals)
	`
CREATE TABLE metric_samples (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	cpu_headroom_pct INTEGER NOT NULL,
	mem_headroom_pct INTEGER NOT NULL,
	disk_headroom_pct INTEGER NOT NULL,
	load1 REAL NOT NULL,
	sampled_at TEXT NOT NULL
);
CREATE INDEX idx_metric_samples_time ON metric_samples(sampled_at);
`,
}
