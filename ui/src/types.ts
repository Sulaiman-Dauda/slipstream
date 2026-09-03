export type SiteType = "wordpress" | "woocommerce" | "static" | "php" | "laravel" | "proxy";
export type Profile = "balanced" | "commerce" | "maximum";

export interface Site {
  id: number;
  domain: string;
  aliases: string[];
  type: SiteType;
  profile: Profile;
  engine: string;
  php_version: string;
  system_user: string;
  status: "provisioning" | "active" | "error" | "deleting";
  staging_of?: number;
  config: {
    cache_enabled: boolean;
    cache_ttl_sec: number;
    object_cache: boolean;
    sftp_enabled: boolean;
    proxy_upstream?: string;
    database: { enabled: boolean; name: string; user: string };
    backups: { enabled: boolean; schedule: string; retention_days: number };
    php: { memory_limit_mb: number; upload_max_mb: number; max_execution_seconds: number };
  };
  created_at: string;
}

export interface Task {
  id: number;
  kind: string;
  site_id: number;
  status: "pending" | "running" | "succeeded" | "failed";
  progress: number;
  message: string;
  log: string;
  error?: string;
  created_at: string;
}

export interface Deployment {
  id: number;
  site_id: number;
  release_id: string;
  status: "created" | "guarding" | "blocked" | "promoted" | "rolled_back";
  checksum: string;
  guard_json?: string;
  created_at: string;
  promoted_at?: string;
}

export interface Backup {
  id: number;
  site_id: number;
  snapshot_id: string;
  repository: string;
  size_bytes: number;
  kind: string;
  verify_status: "pending" | "passed" | "failed";
  verified_at?: string;
  restore_estimate_ms?: number;
  created_at: string;
}

export interface SystemStatus {
  cpu_count: number;
  load1: number;
  mem_total_mb: number;
  mem_available_mb: number;
  disk_total_mb: number;
  disk_free_mb: number;
  nginx_running: boolean;
  mariadb_running: boolean;
  redis_running: boolean;
  agent_version: string;
  uptime_seconds: number;
  cpu_headroom_pct: number;
  mem_headroom_pct: number;
  disk_headroom_pct: number;
}

export interface MetricSample {
  cpu_headroom_pct: number;
  mem_headroom_pct: number;
  disk_headroom_pct: number;
  load1: number;
  sampled_at: string;
}

export interface DriftEvent {
  id: number;
  path: string;
  expected_hash: string;
  actual_hash: string;
  status: string;
  detected_at: string;
}

export interface AuditEvent {
  id: number;
  actor: string;
  action: string;
  subject: string;
  detail: string;
  created_at: string;
}

export interface GuardSample {
  path: string;
  p50_ms: number;
  p95_ms: number;
  avg_queries: number;
  peak_mem_bytes: number;
  errors: number;
  requests: number;
}

export interface GuardReport {
  verdict: "pass" | "warn" | "block";
  reasons?: string[];
  baseline?: GuardSample[];
  candidate?: GuardSample[];
  measured_at: string;
}

export interface CronJob {
  id: number;
  site_id: number;
  schedule: string;
  command: string;
  description: string;
  enabled: boolean;
  last_run?: string;
  last_status: string;
  created_at: string;
}

export interface FileEntry {
  name: string;
  is_dir: boolean;
  size: number;
  mode: string;
  mod_time: string;
}

export interface WPPlugin {
  name: string;
  status: string;
  version: string;
  update: string;
}

export interface ServiceInfo {
  name: string;
  unit: string;
  active: boolean;
  enabled: boolean;
  detail: string;
}

export interface PanelUser {
  id: number;
  email: string;
  role: string;
  site_ids?: number[];
  totp_enabled: boolean;
  created_at: string;
}

export interface UpdateStatus {
  current: string;
  latest?: string;
  update_available: boolean;
  notes_url?: string;
  checked_at?: string;
  check_enabled: boolean;
  error?: string;
}
