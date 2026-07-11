import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, fmtAgo, fmtBytes, fmtDuration } from "../api";
import { Backup, Deployment, GuardReport, Site } from "../types";
import TaskFeed from "../components/TaskFeed";

type Tab = "overview" | "cache" | "deployments" | "backups";

export default function SiteDetail() {
  const { id } = useParams();
  const siteId = Number(id);
  const navigate = useNavigate();

  const [site, setSite] = useState<Site | null>(null);
  const [tab, setTab] = useState<Tab>("overview");
  const [notice, setNotice] = useState("");

  const load = useCallback(() => {
    api.get<Site>(`/api/sites/${siteId}`).then(setSite).catch(() => navigate("/sites"));
  }, [siteId, navigate]);

  useEffect(() => {
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, [load]);

  if (!site) return null;

  const act = async (fn: () => Promise<unknown>, doneMsg: string) => {
    setNotice("");
    try {
      await fn();
      setNotice(doneMsg);
    } catch (e) {
      setNotice(e instanceof Error ? `Error: ${e.message}` : "Error");
    }
  };

  return (
    <>
      <div className="row between">
        <div>
          <h1>
            {site.domain}{" "}
            <span className={`badge ${site.status === "active" ? "good" : site.status === "error" ? "bad" : "accent"}`}>
              {site.status}
            </span>
          </h1>
          <p className="sub">
            {site.type} · {site.profile} profile{site.php_version ? ` · PHP ${site.php_version}` : ""}
            {site.staging_of ? " · staging environment" : ""}
          </p>
        </div>
        <div className="row">
          <a className="btn" href={`https://${site.domain}`} target="_blank" rel="noreferrer">
            Visit ↗
          </a>
          <button
            className="danger"
            onClick={() => {
              if (confirm(`Delete ${site.domain} and all its data? This cannot be undone.`)) {
                act(() => api.del(`/api/sites/${site.id}`), "Deletion started").then(() => navigate("/sites"));
              }
            }}
          >
            Delete
          </button>
        </div>
      </div>

      {notice && <div className={notice.startsWith("Error") ? "error-box" : "card"} style={{ marginBottom: 12 }}>{notice}</div>}

      <div className="tabs">
        {(["overview", "cache", "deployments", "backups"] as Tab[]).map((t) => (
          <button key={t} className={tab === t ? "active" : ""} onClick={() => setTab(t)}>
            {t[0].toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>

      {tab === "overview" && <Overview site={site} act={act} />}
      {tab === "cache" && <CacheTab site={site} act={act} onSaved={load} />}
      {tab === "deployments" && <Deployments site={site} act={act} />}
      {tab === "backups" && <Backups site={site} act={act} />}

      <h2>Activity</h2>
      <TaskFeed siteId={site.id} />
    </>
  );
}

function Overview({ site, act }: { site: Site; act: (fn: () => Promise<unknown>, msg: string) => void }) {
  return (
    <div className="grid cols-3">
      <div className="card">
        <h3>Velocity Engine</h3>
        <div className="stat">{site.config.cache_enabled ? "On" : "Off"}</div>
        <p className="dim">Full-page cache with request coalescing and stale-while-revalidate.</p>
      </div>
      <div className="card">
        <h3>Staging</h3>
        {site.staging_of ? (
          <p className="dim">This is a staging environment.</p>
        ) : (
          <>
            <p className="dim">Clone production to test changes safely, then Safe Push.</p>
            <button
              className="small"
              onClick={() => act(() => api.post(`/api/sites/${site.id}/staging`), "Staging clone started")}
            >
              Create staging
            </button>
          </>
        )}
      </div>
      <div className="card">
        <h3>Certificate</h3>
        <p className="dim">Let's Encrypt via the panel-managed webroot flow.</p>
        <button
          className="small"
          onClick={() => act(() => api.post(`/api/sites/${site.id}/certificate`), "Certificate requested")}
        >
          Issue / renew
        </button>
      </div>
    </div>
  );
}

function CacheTab({
  site,
  act,
  onSaved,
}: {
  site: Site;
  act: (fn: () => Promise<unknown>, msg: string) => void;
  onSaved: () => void;
}) {
  const [profile, setProfile] = useState(site.profile);
  const [enabled, setEnabled] = useState(site.config.cache_enabled);
  const [ttl, setTTL] = useState(site.config.cache_ttl_sec || 0);
  const [purgeURL, setPurgeURL] = useState("");

  const save = () =>
    act(async () => {
      await api.put(`/api/sites/${site.id}/config`, {
        profile,
        cache_enabled: enabled,
        cache_ttl_sec: Number(ttl) || 0,
      });
      onSaved();
    }, "Configuration applied — Nginx reloaded gracefully");

  return (
    <div className="grid" style={{ gridTemplateColumns: "1fr 1fr", alignItems: "start" }}>
      <div className="card">
        <h3>Cache policy</h3>
        <label>Profile</label>
        <select value={profile} onChange={(e) => setProfile(e.target.value as Site["profile"])}>
          <option value="balanced">Balanced</option>
          <option value="commerce">Commerce</option>
          <option value="maximum">Maximum</option>
        </select>
        <label>Page cache</label>
        <select value={enabled ? "on" : "off"} onChange={(e) => setEnabled(e.target.value === "on")}>
          <option value="on">Enabled</option>
          <option value="off">Disabled</option>
        </select>
        <label>TTL override (seconds, 0 = profile default)</label>
        <input type="number" min={0} value={ttl} onChange={(e) => setTTL(Number(e.target.value))} />
        <button style={{ marginTop: 18 }} onClick={save}>
          Apply
        </button>
      </div>
      <div className="card">
        <h3>Purge</h3>
        <p className="dim">
          Content changes purge precisely via the WordPress connector. Manual purges are here for
          everything else.
        </p>
        <label>Purge one URL</label>
        <div className="row">
          <input
            value={purgeURL}
            onChange={(e) => setPurgeURL(e.target.value)}
            placeholder={`https://${site.domain}/post/`}
          />
          <button
            className="small"
            onClick={() =>
              act(() => api.post(`/api/sites/${site.id}/purge`, { urls: [purgeURL] }), "URL purged")
            }
          >
            Purge
          </button>
        </div>
        <button
          className="ghost"
          style={{ marginTop: 16 }}
          onClick={() => act(() => api.post(`/api/sites/${site.id}/purge`), "Entire cache purged")}
        >
          Purge everything
        </button>
      </div>
    </div>
  );
}

const deployBadge: Record<Deployment["status"], string> = {
  created: "dim",
  guarding: "accent",
  blocked: "bad",
  promoted: "good",
  rolled_back: "warn",
};

function Deployments({ site, act }: { site: Site; act: (fn: () => Promise<unknown>, msg: string) => void }) {
  const [deps, setDeps] = useState<Deployment[]>([]);
  const [expanded, setExpanded] = useState<number | null>(null);

  const load = useCallback(() => {
    api.get<Deployment[]>(`/api/sites/${site.id}/deployments`).then((d) => setDeps(d ?? [])).catch(() => undefined);
  }, [site.id]);

  useEffect(() => {
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, [load]);

  return (
    <>
      <div className="row" style={{ marginBottom: 16 }}>
        {!site.staging_of && (
          <button
            onClick={() =>
              act(
                () => api.post(`/api/sites/${site.id}/safe-push`),
                "Safe Push started — Performance Guard is comparing staging against production",
              )
            }
          >
            Safe Push from staging
          </button>
        )}
        <button
          className="ghost"
          onClick={() => act(() => api.post(`/api/sites/${site.id}/rollback`), "Rolling back to previous release")}
        >
          Instant rollback
        </button>
      </div>

      {deps.length === 0 ? (
        <p className="dim">No releases yet. Safe Push from staging or deploy with slipctl.</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Release</th>
              <th>Status</th>
              <th>Guard</th>
              <th>Created</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {deps.map((d) => {
              let guard: GuardReport | null = null;
              try {
                guard = d.guard_json ? (JSON.parse(d.guard_json) as GuardReport) : null;
              } catch {
                guard = null;
              }
              return (
                <>
                  <tr key={d.id}>
                    <td className="mono">{d.release_id}</td>
                    <td>
                      <span className={`badge ${deployBadge[d.status]}`}>{d.status.replace("_", " ")}</span>
                    </td>
                    <td>
                      {guard ? (
                        <button className="ghost small" onClick={() => setExpanded(expanded === d.id ? null : d.id)}>
                          {guard.verdict} {guard.reasons?.length ? `(${guard.reasons.length})` : ""}
                        </button>
                      ) : (
                        <span className="dim">—</span>
                      )}
                    </td>
                    <td className="dim">{fmtAgo(d.created_at)}</td>
                    <td>
                      {d.status === "created" && (
                        <button
                          className="small"
                          onClick={() => act(() => api.post(`/api/deployments/${d.id}/promote`), "Promoting release")}
                        >
                          Promote
                        </button>
                      )}
                    </td>
                  </tr>
                  {expanded === d.id && guard?.reasons && (
                    <tr key={`${d.id}-guard`}>
                      <td colSpan={5}>
                        <pre className="log">{guard.reasons.join("\n")}</pre>
                      </td>
                    </tr>
                  )}
                </>
              );
            })}
          </tbody>
        </table>
      )}
    </>
  );
}

const verifyBadge: Record<Backup["verify_status"], string> = {
  pending: "dim",
  passed: "good",
  failed: "bad",
};

function Backups({ site, act }: { site: Site; act: (fn: () => Promise<unknown>, msg: string) => void }) {
  const [backups, setBackups] = useState<Backup[]>([]);

  const load = useCallback(() => {
    api.get<Backup[]>(`/api/sites/${site.id}/backups`).then((b) => setBackups(b ?? [])).catch(() => undefined);
  }, [site.id]);

  useEffect(() => {
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, [load]);

  const latest = backups[0];

  return (
    <>
      <div className="grid cols-3" style={{ marginBottom: 16 }}>
        <div className="card">
          <h3>Last backup</h3>
          <div className="stat" style={{ fontSize: 18 }}>{latest ? fmtAgo(latest.created_at) : "never"}</div>
        </div>
        <div className="card">
          <h3>Last restore test</h3>
          <div className="stat" style={{ fontSize: 18 }}>
            {latest?.verify_status === "passed" ? "Passed" : latest?.verify_status === "failed" ? "Failed" : "—"}
          </div>
        </div>
        <div className="card">
          <h3>Recovery estimate</h3>
          <div className="stat" style={{ fontSize: 18 }}>
            {latest?.restore_estimate_ms ? fmtDuration(latest.restore_estimate_ms) : "—"}
          </div>
        </div>
      </div>

      <button onClick={() => act(() => api.post(`/api/sites/${site.id}/backups`), "Backup started")}>
        Back up now
      </button>

      {backups.length > 0 && (
        <table style={{ marginTop: 16 }}>
          <thead>
            <tr>
              <th>Snapshot</th>
              <th>Size</th>
              <th>Restore test</th>
              <th>Created</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {backups.map((b) => (
              <tr key={b.id}>
                <td className="mono">{b.snapshot_id.slice(0, 12)}</td>
                <td>{fmtBytes(b.size_bytes)}</td>
                <td>
                  <span className={`badge ${verifyBadge[b.verify_status]}`}>{b.verify_status}</span>
                  {b.verified_at && <span className="dim" style={{ marginLeft: 8 }}>{fmtAgo(b.verified_at)}</span>}
                </td>
                <td className="dim">{fmtAgo(b.created_at)}</td>
                <td>
                  <button
                    className="ghost small"
                    onClick={() => act(() => api.post(`/api/backups/${b.id}/verify`), "Restore test started")}
                  >
                    Test restore
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
