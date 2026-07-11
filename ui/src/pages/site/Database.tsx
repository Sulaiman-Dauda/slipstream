import { useEffect, useState } from "react";
import { api } from "../../api";
import { Site } from "../../types";
import { CopyButton, useAction, useToast } from "../../components/ui";
import { Icon } from "../../icons";

interface QueryResult { columns: string[]; rows: string[][]; message?: string }
interface DBInfo { database: string; user: string; tables: QueryResult }

export default function Database({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [info, setInfo] = useState<DBInfo | null>(null);
  const [sql, setSql] = useState("SELECT * FROM wp_options LIMIT 20;");
  const [allowWrites, setAllowWrites] = useState(false);
  const [result, setResult] = useState<QueryResult | null>(null);

  useEffect(() => {
    api.get<DBInfo>(`/api/sites/${site.id}/database`).then(setInfo).catch(() => undefined);
  }, [site.id]);

  const runQuery = () =>
    run(async () => setResult(await api.post<QueryResult>(`/api/sites/${site.id}/database/query`, { sql, allow_writes: allowWrites })));

  const launchAdminer = () => {
    const tab = window.open("about:blank", "_blank");
    run(async () => {
      const r = await api.post<{ url: string }>(`/api/sites/${site.id}/database/adminer`);
      if (tab) tab.location.href = r.url;
      else window.location.href = r.url;
    }, "Database console opened (expires in 30 minutes)").then((ok) => { if (!ok && tab) tab.close(); });
  };

  const importDatabase = () => {
    const path = prompt("Path to an uploaded .sql file under Files", "shared/import.sql")?.trim();
    if (!path) return;
    const confirm = prompt(`This replaces the database and creates a rollback dump first. Type ${site.domain} to continue:`);
    if (confirm !== site.domain) return;
    run(() => api.post(`/api/sites/${site.id}/database/import`, { path, confirm }), "Database import started");
  };

  if (!site.config.database.enabled) return <div className="info-box">This site has no managed database.</div>;

  const table = result || info?.tables;

  return (
    <>
      <div className="grid cols-2 mb stagger">
        <div className="card">
          <div className="card-head"><span className="card-ico"><Icon.database /></span><h3 style={{ margin: 0 }}>Database</h3></div>
          <dl className="kv mt">
            <dt>Name</dt><dd className="mono">{info?.database || site.config.database.name}<CopyButton value={info?.database || site.config.database.name} /></dd>
            <dt>User</dt><dd className="mono">{info?.user || site.config.database.user}<CopyButton value={info?.user || site.config.database.user} /></dd>
          </dl>
        </div>
        <div className="card">
          <div className="card-head"><span className="card-ico"><Icon.terminal /></span><h3 style={{ margin: 0 }}>Tools</h3></div>
          <div className="row mt">
            <button onClick={launchAdminer} disabled={busy}><Icon.external /> Open database console</button>
            <button className="ghost" onClick={() => run(() => api.post(`/api/sites/${site.id}/database/export`), "Export started — find it under Files › shared/exports")}><Icon.download /> Export .sql</button>
            <button className="ghost" onClick={importDatabase} disabled={busy}><Icon.upload /> Import .sql</button>
          </div>
          <p className="note tiny">The console (Adminer) opens on a private link that self-destructs after 30 minutes.</p>
        </div>
      </div>

      <div className="card">
        <div className="card-head"><span className="card-ico"><Icon.code /></span><h3 style={{ margin: 0 }}>Query console</h3></div>
        <textarea value={sql} onChange={(e) => setSql(e.target.value)} rows={4} className="mt" />
        <div className="row between mt">
          <label className="row" style={{ margin: 0, gap: 8, fontWeight: 500 }}>
            <span className="switch"><input type="checkbox" checked={allowWrites} onChange={(e) => setAllowWrites(e.target.checked)} /><span className="slider" /></span>
            Allow write statements
          </label>
          <button onClick={runQuery} disabled={busy}>{busy ? "Running…" : "Run query"}</button>
        </div>
        {toast.node}
      </div>

      {table && (
        <div className="mt">
          {table.message && <div className="info-box mb">{table.message}</div>}
          {table.columns && table.columns.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead><tr>{table.columns.map((c) => <th key={c}>{c}</th>)}</tr></thead>
                <tbody>
                  {(table.rows || []).map((row, i) => (
                    <tr key={i}>{row.map((cell, j) => <td key={j} className="mono" style={{ maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{cell}</td>)}</tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </>
  );
}
