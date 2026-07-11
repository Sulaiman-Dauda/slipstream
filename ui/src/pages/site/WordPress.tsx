import { useEffect, useState } from "react";
import { api } from "../../api";
import { Site, WPPlugin } from "../../types";
import { useAction, useToast } from "../../components/ui";

interface CacheStats { backend: string; hits: number; misses: number; entries: number; mem_used: number; mem_total: number; hit_rate_pct: number }

export default function WordPress({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [plugins, setPlugins] = useState<WPPlugin[]>([]);
  const [themes, setThemes] = useState<WPPlugin[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [stats, setStats] = useState<CacheStats | null>(null);

  const load = () => {
    api.get<{ plugins: WPPlugin[]; themes: WPPlugin[] }>(`/api/sites/${site.id}/wp/plugins`)
      .then((r) => { setPlugins(r.plugins || []); setThemes(r.themes || []); setLoaded(true); })
      .catch(() => setLoaded(true));
    api.get<CacheStats>(`/api/sites/${site.id}/cache-stats`).then(setStats).catch(() => undefined);
  };

  useEffect(() => { load(); /* eslint-disable-next-line */ }, [site.id]);

  const magicLogin = () => {
    // Open the tab synchronously (inside the click) so it isn't blocked, then
    // point it at the login URL once the request returns.
    const tab = window.open("about:blank", "_blank");
    run(async () => {
      const r = await api.post<{ url: string }>(`/api/sites/${site.id}/wp/login`);
      if (tab) tab.location.href = r.url;
      else window.location.href = r.url;
    }).then((ok) => { if (!ok && tab) tab.close(); });
  };

  const update = (what: string) => run(() => api.post(`/api/sites/${site.id}/wp/update`, { what }), `Updating ${what}…`);
  const toggleCache = (enable: boolean) => run(() => api.post(`/api/sites/${site.id}/wp/object-cache`, { enable }), enable ? "Enabling Redis object cache…" : "Disabling object cache…");

  const withUpdates = plugins.filter((p) => p.update && p.update !== "none").length;

  return (
    <>
      <div className="grid cols-3 mb">
        <div className="card">
          <h3>Admin access</h3>
          <p className="dim" style={{ fontSize: 13 }}>Log into wp-admin with one click — no password needed.</p>
          <button className="mt" onClick={magicLogin} disabled={busy}>Log in to WordPress ↗</button>
        </div>
        <div className="card">
          <h3>Updates</h3>
          <p className="dim" style={{ fontSize: 13 }}>{withUpdates > 0 ? `${withUpdates} plugin update${withUpdates === 1 ? "" : "s"} available` : "Everything up to date"}</p>
          <div className="row mt">
            <button className="small" onClick={() => update("all")} disabled={busy}>Update all</button>
            <button className="ghost small" onClick={() => update("core")} disabled={busy}>Core</button>
            <button className="ghost small" onClick={() => update("plugins")} disabled={busy}>Plugins</button>
          </div>
        </div>
        <div className="card">
          <h3>Object cache {stats && stats.backend !== "none" && <span className="badge good plain" style={{ marginLeft: 6 }}>{stats.backend}</span>}</h3>
          <p className="dim" style={{ fontSize: 13 }}>In-memory cache for DB queries — faster admin, cart, and cold renders. APCu on a single server (no daemon).</p>
          {stats && stats.backend === "apcu" && (
            <div className="dim3" style={{ fontSize: 12, margin: "6px 0" }}>
              hit rate {stats.hit_rate_pct}% · {stats.entries} entries · {(stats.mem_used / 1048576).toFixed(1)}MB used
            </div>
          )}
          <div className="row mt">
            <button className="small" onClick={() => toggleCache(true)} disabled={busy || site.config.object_cache}>Enable</button>
            <button className="ghost small" onClick={() => toggleCache(false)} disabled={busy || !site.config.object_cache}>Disable</button>
          </div>
        </div>
      </div>

      <div className="card mb">
        <div className="row between">
          <div>
            <h3 style={{ margin: 0 }}>Cache warming</h3>
            <p className="dim" style={{ fontSize: 13, margin: "4px 0 0" }}>Pre-fill the page cache from your sitemap so the first visitor is never cold. Runs automatically after deploys.</p>
          </div>
          <button className="small" disabled={busy} onClick={() => run(() => api.post(`/api/sites/${site.id}/warm`), "Warming cache from sitemap…")}>Warm now</button>
        </div>
      </div>

      {toast.node}

      <h2>Plugins</h2>
      {!loaded ? <span className="spinner" /> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Plugin</th><th>Status</th><th>Version</th><th>Update</th></tr></thead>
            <tbody>
              {plugins.map((p) => (
                <tr key={p.name}>
                  <td className="mono">{p.name}</td>
                  <td><span className={`badge ${p.status === "active" ? "good" : "dim"}`}>{p.status}</span></td>
                  <td>{p.version}</td>
                  <td>{p.update && p.update !== "none" ? <span className="badge warn">{p.update} available</span> : <span className="dim3">—</span>}</td>
                </tr>
              ))}
              {plugins.length === 0 && <tr><td colSpan={4} className="dim">No plugins found.</td></tr>}
            </tbody>
          </table>
        </div>
      )}

      {themes.length > 0 && (
        <>
          <h2>Themes</h2>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Theme</th><th>Status</th><th>Version</th><th>Update</th></tr></thead>
              <tbody>
                {themes.map((t) => (
                  <tr key={t.name}>
                    <td className="mono">{t.name}</td>
                    <td><span className={`badge ${t.status === "active" ? "good" : "dim"}`}>{t.status}</span></td>
                    <td>{t.version}</td>
                    <td>{t.update && t.update !== "none" ? <span className="badge warn">{t.update}</span> : <span className="dim3">—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </>
  );
}
