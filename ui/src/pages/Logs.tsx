import { useCallback, useEffect, useState } from "react";
import { api } from "../api";

const sources = [
  { v: "nginx-access", l: "Nginx access" },
  { v: "nginx-error", l: "Nginx error" },
  { v: "php-error", l: "PHP-FPM" },
  { v: "mariadb", l: "MariaDB" },
  { v: "api", l: "Panel API" },
  { v: "agent", l: "Panel agent" },
];

export default function Logs() {
  const [source, setSource] = useState("nginx-error");
  const [content, setContent] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    api.get<{ content: string }>(`/api/logs?source=${encodeURIComponent(source)}&lines=300`)
      .then((r) => setContent(r.content || "(empty)"))
      .catch((e) => setContent(e instanceof Error ? e.message : "error"))
      .finally(() => setLoading(false));
  }, [source]);

  useEffect(() => { load(); }, [load]);

  return (
    <>
      <div className="topbar">
        <div><h1>Logs</h1><p className="sub">System and service logs. Per-site logs live on each site's Logs tab.</p></div>
        <button className="ghost" onClick={load}>↻ Refresh</button>
      </div>
      <div className="tabs">
        {sources.map((s) => <button key={s.v} className={source === s.v ? "active" : ""} onClick={() => setSource(s.v)}>{s.l}</button>)}
      </div>
      {loading ? <div className="empty"><span className="spinner" /></div> : <pre className="log">{content}</pre>}
    </>
  );
}
