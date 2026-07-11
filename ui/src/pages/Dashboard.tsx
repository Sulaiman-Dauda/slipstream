import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { DriftEvent, Site, SystemStatus } from "../types";
import { usePoll } from "../components/ui";
import TaskFeed from "../components/TaskFeed";

interface ServiceInfo { name: string; active: boolean }

function Meter({ label, pct }: { label: string; pct: number }) {
  const cls = pct < 15 ? "bad" : pct < 35 ? "warn" : "";
  return (
    <div className="card">
      <h3>{label}</h3>
      <div className="stat">{pct}<small>%</small> <small style={{ fontWeight: 400 }}>headroom</small></div>
      <div className={`meter ${cls}`}><div style={{ width: `${Math.max(2, Math.min(100, pct))}%` }} /></div>
    </div>
  );
}

export default function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [drift, setDrift] = useState<DriftEvent[]>([]);
  const [agentError, setAgentError] = useState("");
  const [services] = usePoll<ServiceInfo[]>("/api/services", 15000);
  const [sites] = usePoll<Site[]>("/api/sites", 15000);

  const load = useCallback(() => {
    api.get<SystemStatus>("/api/system/status").then((s) => { setStatus(s); setAgentError(""); })
      .catch((e) => setAgentError(e instanceof Error ? e.message : "agent unavailable"));
    api.get<DriftEvent[]>("/api/system/drift").then((d) => setDrift(d ?? [])).catch(() => undefined);
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, [load]);

  const resolveDrift = async (id: number, action: "restore" | "accept") => {
    await api.post(`/api/system/drift/${id}/resolve`, { action });
    load();
  };

  const activeSites = (sites || []).filter((s) => !s.staging_of).length;

  return (
    <>
      <div className="topbar">
        <div>
          <h1>Dashboard</h1>
          <p className="sub">Server health and capacity at a glance.</p>
        </div>
        <Link className="btn" to="/sites">Manage sites →</Link>
      </div>

      {agentError && <div className="error-box">Agent unreachable: {agentError}</div>}

      {status && (
        <div className="grid cols-4">
          <Meter label="CPU" pct={status.cpu_headroom_pct} />
          <Meter label="Memory" pct={status.mem_headroom_pct} />
          <Meter label="Disk" pct={status.disk_headroom_pct} />
          <div className="card">
            <h3>Overview</h3>
            <div className="stat-sm">{activeSites} site{activeSites === 1 ? "" : "s"}</div>
            <div className="dim" style={{ fontSize: 13, marginTop: 6 }}>
              {status.cpu_count} cores · load {status.load1.toFixed(2)}<br />
              agent {status.agent_version}
            </div>
          </div>
        </div>
      )}

      <div className="row between" style={{ marginTop: 28, marginBottom: 12 }}>
        <h2 style={{ margin: 0 }}>Services</h2>
        <Link to="/services" className="dim" style={{ fontSize: 12.5 }}>Manage →</Link>
      </div>
      <div className="row" style={{ gap: 8 }}>
        {(services || []).map((s) => (
          <span key={s.name} className={`badge ${s.active ? "good" : "bad"}`}>{s.name}</span>
        ))}
        {(!services || services.length === 0) && <span className="dim">Loading…</span>}
      </div>

      {drift.length > 0 && (
        <>
          <h2>Configuration drift</h2>
          <p className="sub">These managed files changed outside the panel. Nothing was overwritten.</p>
          {drift.map((d) => (
            <div className="task-row" key={d.id} style={{ marginBottom: 8 }}>
              <span className="mono msg">{d.path}</span>
              <button className="small" onClick={() => resolveDrift(d.id, "restore")}>Restore</button>
              <button className="ghost small" onClick={() => resolveDrift(d.id, "accept")}>Keep change</button>
            </div>
          ))}
        </>
      )}

      <h2>Recent activity</h2>
      <TaskFeed />

      {status && drift.length === 0 && (
        <div className="mt">
          <span className="badge good plain">✓ No configuration drift — everything matches the panel</span>
        </div>
      )}
    </>
  );
}
