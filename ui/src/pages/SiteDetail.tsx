import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, fmtAgo, fmtBytes, fmtDuration } from "../api";
import { Backup, Deployment, GuardReport, Site } from "../types";
import { StatusBadge, useAction, useToast, usePoll } from "../components/ui";
import GuardReportView from "../components/GuardReport";
import TaskFeed from "../components/TaskFeed";
import Database from "./site/Database";
import Files from "./site/Files";
import WordPress from "./site/WordPress";
import Cron from "./site/Cron";

type Tab = "overview" | "cache" | "wordpress" | "deployments" | "backups" | "database" | "files" | "cron" | "php" | "sftp" | "logs";

export default function SiteDetail() {
  const { id } = useParams();
  const siteId = Number(id);
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const toast = useToast();
  const { run } = useAction(toast);
  const [site, setSite] = useState<Site | null>(null);
  const tab = (params.get("tab") as Tab) || "overview";
  const setTab = (t: Tab) => setParams({ tab: t });

  const load = useCallback(() => {
    api.get<Site>(`/api/sites/${siteId}`).then(setSite).catch(() => navigate("/sites"));
  }, [siteId, navigate]);
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t); }, [load]);

  if (!site) return <div className="empty"><span className="spinner" /></div>;

  const isWP = site.type === "wordpress" || site.type === "woocommerce";
  const hasPHP = !!site.php_version;
  const tabs: [Tab, string][] = [
    ["overview", "Overview"], ["cache", "Cache"],
    ...(isWP ? [["wordpress", "WordPress"] as [Tab, string]] : []),
    ["deployments", "Deployments"], ["backups", "Backups"],
    ...(site.config.database.enabled ? [["database", "Database"] as [Tab, string]] : []),
    ["files", "Files"], ["cron", "Cron"],
    ...(hasPHP ? [["php", "PHP"] as [Tab, string]] : []),
    ["sftp", "SFTP"], ["logs", "Logs"],
  ];

  return (
    <>
      <div className="topbar">
        <div>
          <h1>{site.domain} <StatusBadge status={site.status} /></h1>
          <p className="sub">{site.type} · {site.profile} profile{site.php_version ? ` · PHP ${site.php_version}` : ""}{site.staging_of ? " · staging" : ""}</p>
        </div>
        <div className="row">
          <a className="btn ghost" href={`https://${site.domain}`} target="_blank" rel="noreferrer">Visit ↗</a>
          <button className="danger" onClick={() => { if (confirm(`Delete ${site.domain} and all its data? This cannot be undone.`)) run(() => api.del(`/api/sites/${site.id}`), "Deletion started").then(() => navigate("/sites")); }}>Delete</button>
        </div>
      </div>

      {toast.node}

      <div className="tabs">
        {tabs.map(([t, label]) => <button key={t} className={tab === t ? "active" : ""} onClick={() => setTab(t)}>{label}</button>)}
      </div>

      {tab === "overview" && <Overview site={site} run={run} onChange={load} />}
      {tab === "cache" && <CacheTab site={site} onChange={load} />}
      {tab === "wordpress" && <WordPress site={site} />}
      {tab === "deployments" && <Deployments site={site} run={run} />}
      {tab === "backups" && <Backups site={site} run={run} />}
      {tab === "database" && <Database site={site} />}
      {tab === "files" && <Files site={site} />}
      {tab === "cron" && <Cron site={site} />}
      {tab === "php" && <PHPTab site={site} onChange={load} />}
      {tab === "sftp" && <SFTPTab site={site} onChange={load} />}
      {tab === "logs" && <LogsTab site={site} />}

      <h2>Activity</h2>
      <TaskFeed siteId={site.id} />
    </>
  );
}

type RunFn = (fn: () => Promise<unknown>, ok?: string) => Promise<boolean>;

function Overview({ site, run, onChange }: { site: Site; run: RunFn; onChange: () => void }) {
  return (
    <div className="grid cols-3">
      <div className="card">
        <h3>Velocity Engine</h3>
        <div className="stat-sm">{site.config.cache_enabled ? "On" : "Off"}</div>
        <p className="dim" style={{ fontSize: 13 }}>Full-page cache with coalescing and stale-while-revalidate.</p>
      </div>
      <div className="card">
        <h3>Staging</h3>
        {site.staging_of ? <p className="dim">This is a staging environment.</p> : (
          <><p className="dim" style={{ fontSize: 13 }}>Clone production to test changes, then Safe Push.</p>
          <button className="small mt" onClick={() => run(() => api.post(`/api/sites/${site.id}/staging`), "Staging clone started").then(onChange)}>Create staging</button></>
        )}
      </div>
      <div className="card">
        <h3>Certificate</h3>
        <p className="dim" style={{ fontSize: 13 }}>Automatic Let's Encrypt, auto-renewed.</p>
        <button className="small mt" onClick={() => run(() => api.post(`/api/sites/${site.id}/certificate`), "Certificate requested")}>Issue / renew</button>
      </div>
    </div>
  );
}

function CacheTab({ site, onChange }: { site: Site; onChange: () => void }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [profile, setProfile] = useState(site.profile);
  const [enabled, setEnabled] = useState(site.config.cache_enabled);
  const [ttl, setTTL] = useState(site.config.cache_ttl_sec || 0);
  const [purgeURL, setPurgeURL] = useState("");

  return (
    <div className="grid cols-2">
      <div className="card">
        <h3>Cache policy</h3>
        <label>Profile</label>
        <select value={profile} onChange={(e) => setProfile(e.target.value as Site["profile"])}>
          <option value="balanced">Balanced</option><option value="commerce">Commerce</option><option value="maximum">Maximum</option>
        </select>
        <label>Page cache</label>
        <select value={enabled ? "on" : "off"} onChange={(e) => setEnabled(e.target.value === "on")}><option value="on">Enabled</option><option value="off">Disabled</option></select>
        <label>TTL override <span className="hint">seconds, 0 = profile default</span></label>
        <input type="number" min={0} value={ttl} onChange={(e) => setTTL(Number(e.target.value))} />
        {toast.node}
        <button className="mt" disabled={busy} onClick={() => run(() => api.put(`/api/sites/${site.id}/config`, { profile, cache_enabled: enabled, cache_ttl_sec: Number(ttl) || 0 }), "Configuration applied").then(onChange)}>Apply</button>
      </div>
      <div className="card">
        <h3>Purge</h3>
        <p className="dim" style={{ fontSize: 13 }}>Content changes purge precisely via the WordPress connector. Manual purges are here for everything else.</p>
        <label>Purge one URL</label>
        <div className="row">
          <input value={purgeURL} onChange={(e) => setPurgeURL(e.target.value)} placeholder={`https://${site.domain}/post/`} />
          <button className="small" onClick={() => run(() => api.post(`/api/sites/${site.id}/purge`, { urls: [purgeURL] }), "URL purged")}>Purge</button>
        </div>
        <button className="ghost mt" onClick={() => run(() => api.post(`/api/sites/${site.id}/purge`), "Entire cache purged")}>Purge everything</button>
      </div>
    </div>
  );
}

function Deployments({ site, run }: { site: Site; run: RunFn }) {
  const [deps] = usePoll<Deployment[]>(`/api/sites/${site.id}/deployments`, 8000);
  const [expanded, setExpanded] = useState<number | null>(null);
  const badge: Record<string, string> = { created: "dim", guarding: "accent", blocked: "bad", promoted: "good", rolled_back: "warn" };

  return (
    <>
      <div className="row mb">
        {!site.staging_of && <button onClick={() => run(() => api.post(`/api/sites/${site.id}/safe-push`), "Safe Push started — Performance Guard is comparing staging vs production")}>Safe Push from staging</button>}
        <button className="ghost" onClick={() => run(() => api.post(`/api/sites/${site.id}/rollback`), "Rolling back to previous release")}>Instant rollback</button>
      </div>
      {!deps || deps.length === 0 ? <div className="info-box">No releases yet. Safe Push from staging, or deploy with slipctl.</div> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Release</th><th>Status</th><th>Guard</th><th>Created</th></tr></thead>
            <tbody>
              {deps.map((d) => {
                let guard: GuardReport | null = null;
                try { guard = d.guard_json ? JSON.parse(d.guard_json) : null; } catch { guard = null; }
                return (
                  <>
                    <tr key={d.id}>
                      <td className="mono">{d.release_id}</td>
                      <td><span className={`badge ${badge[d.status] || "dim"}`}>{d.status.replace("_", " ")}</span></td>
                      <td>{guard ? <button className="ghost tiny" onClick={() => setExpanded(expanded === d.id ? null : d.id)}>{guard.verdict} ▾</button> : <span className="dim3">—</span>}</td>
                      <td className="dim">{fmtAgo(d.created_at)}</td>
                    </tr>
                    {expanded === d.id && guard && <tr key={`${d.id}-g`}><td colSpan={4} style={{ background: "var(--bg-2)" }}><GuardReportView report={guard} /></td></tr>}
                  </>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function Backups({ site, run }: { site: Site; run: RunFn }) {
  const [backups] = usePoll<Backup[]>(`/api/sites/${site.id}/backups`, 8000);
  const latest = backups?.[0];
  const badge: Record<string, string> = { pending: "dim", passed: "good", failed: "bad" };
  return (
    <>
      <div className="grid cols-3 mb">
        <div className="card"><h3>Last backup</h3><div className="stat-sm">{latest ? fmtAgo(latest.created_at) : "never"}</div></div>
        <div className="card"><h3>Last restore test</h3><div className="stat-sm">{latest?.verify_status === "passed" ? "✓ Passed" : latest?.verify_status === "failed" ? "✗ Failed" : "—"}</div></div>
        <div className="card"><h3>Recovery estimate</h3><div className="stat-sm">{latest?.restore_estimate_ms ? fmtDuration(latest.restore_estimate_ms) : "—"}</div></div>
      </div>
      <button onClick={() => run(() => api.post(`/api/sites/${site.id}/backups`), "Backup started")}>Back up now</button>
      {backups && backups.length > 0 && (
        <div className="table-wrap mt">
          <table>
            <thead><tr><th>Snapshot</th><th>Size</th><th>Restore test</th><th>Created</th><th></th></tr></thead>
            <tbody>
              {backups.map((b) => (
                <tr key={b.id}>
                  <td className="mono">{b.snapshot_id.slice(0, 12)}</td>
                  <td>{fmtBytes(b.size_bytes)}</td>
                  <td><span className={`badge ${badge[b.verify_status]}`}>{b.verify_status}</span>{b.verified_at && <span className="dim3" style={{ marginLeft: 8, fontSize: 12 }}>{fmtAgo(b.verified_at)}</span>}</td>
                  <td className="dim">{fmtAgo(b.created_at)}</td>
                  <td><button className="ghost tiny" onClick={() => run(() => api.post(`/api/backups/${b.id}/verify`), "Restore test started")}>Test restore</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function PHPTab({ site, onChange }: { site: Site; onChange: () => void }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [version, setVersion] = useState(site.php_version);
  const [mem, setMem] = useState(site.config.php.memory_limit_mb || 256);
  const [upload, setUpload] = useState(site.config.php.upload_max_mb || 64);
  const [exec, setExec] = useState(site.config.php.max_execution_seconds || 120);
  return (
    <div className="card form-narrow">
      <h3>PHP settings</h3>
      <label>PHP version</label>
      <select value={version} onChange={(e) => setVersion(e.target.value)}>
        {["8.2", "8.3", "8.4", "8.5"].map((v) => <option key={v} value={v}>PHP {v}</option>)}
      </select>
      <label>Memory limit (MB) <span className="hint">64–4096</span></label>
      <input type="number" value={mem} onChange={(e) => setMem(Number(e.target.value))} />
      <label>Max upload size (MB) <span className="hint">1–2048</span></label>
      <input type="number" value={upload} onChange={(e) => setUpload(Number(e.target.value))} />
      <label>Max execution time (s) <span className="hint">10–600</span></label>
      <input type="number" value={exec} onChange={(e) => setExec(Number(e.target.value))} />
      {toast.node}
      <button className="mt" disabled={busy} onClick={() => run(() => api.put(`/api/sites/${site.id}/php`, { php_version: version, memory_limit_mb: mem, upload_max_mb: upload, max_execution_seconds: exec }), "PHP settings applied").then(onChange)}>Apply</button>
    </div>
  );
}

function SFTPTab({ site, onChange }: { site: Site; onChange: () => void }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [password, setPassword] = useState("");
  const [info, setInfo] = useState<{ host: string; username: string; port: number } | null>(null);
  return (
    <div className="card form-narrow">
      <h3>SFTP access</h3>
      <p className="dim" style={{ fontSize: 13 }}>Enable a chrooted SFTP account for this site. Connect with FileZilla, Cyberduck, or VS Code — jailed to this site only, no shell.</p>
      {site.config.sftp_enabled && <div className="ok-box">SFTP is enabled for <span className="mono">{site.system_user || `slip-site-${site.id}`}</span>.</div>}
      <label>Set SFTP password <span className="hint">12+ characters</span></label>
      <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} />
      {info && <div className="info-box mt"><div className="kv"><dt>Host</dt><dd className="mono">{info.host.split(":")[0]}</dd><dt>Port</dt><dd className="mono">{info.port}</dd><dt>Username</dt><dd className="mono">{info.username}</dd></div></div>}
      {toast.node}
      <div className="row mt">
        <button disabled={busy || password.length < 12} onClick={() => run(async () => { const r = await api.post<{ host: string; username: string; port: number }>(`/api/sites/${site.id}/sftp`, { enable: true, password }); setInfo(r); }, "SFTP enabled").then(onChange)}>Enable SFTP</button>
        {site.config.sftp_enabled && <button className="ghost" onClick={() => run(() => api.post(`/api/sites/${site.id}/sftp`, { enable: false }), "SFTP disabled").then(onChange)}>Disable</button>}
      </div>
    </div>
  );
}

function LogsTab({ site }: { site: Site }) {
  const [source, setSource] = useState("access");
  const [content, setContent] = useState("");
  const load = useCallback(() => {
    const s = `site:${site.domain}:${source}`;
    api.get<{ content: string }>(`/api/logs?source=${encodeURIComponent(s)}&site=${encodeURIComponent(site.domain)}&lines=200`)
      .then((r) => setContent(r.content || "(empty)")).catch((e) => setContent(e instanceof Error ? e.message : "error"));
  }, [site.domain, source]);
  useEffect(() => { load(); }, [load]);
  return (
    <>
      <div className="row mb">
        {[["access", "Access"], ["error", "Error"], ["php", "PHP errors"]].map(([v, l]) => (
          <button key={v} className={source === v ? "" : "ghost"} onClick={() => setSource(v)}>{l}</button>
        ))}
        <button className="ghost small" onClick={load}>↻ Refresh</button>
      </div>
      <pre className="log">{content}</pre>
    </>
  );
}
