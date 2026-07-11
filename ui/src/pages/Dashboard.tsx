import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { DriftEvent, Site, SystemStatus } from "../types";
import { useAction, usePoll, useToast } from "../components/ui";
import { Icon } from "../icons";
import TaskFeed from "../components/TaskFeed";

interface ServiceInfo { name: string; active: boolean }

function Meter({ label, icon, pct }: { label: string; icon: keyof typeof Icon; pct: number }) {
  const cls = pct < 15 ? "bad" : pct < 35 ? "warn" : "";
  const IconCmp = Icon[icon];
  return (
    <div className="card">
      <div className="card-head"><span className={`card-ico ${cls || "good"}`}><IconCmp /></span><h3 style={{ margin: 0 }}>{label}</h3></div>
      <div className="stat">{pct}<small>%</small> <small style={{ fontWeight: 400 }}>headroom</small></div>
      <div className={`meter ${cls}`}><div style={{ width: `${Math.max(2, Math.min(100, pct))}%` }} /></div>
    </div>
  );
}

export default function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [drift, setDrift] = useState<DriftEvent[]>([]);
  const [agentError, setAgentError] = useState("");
  const { data: services } = usePoll<ServiceInfo[]>("/api/services", 15000);
  const { data: sites } = usePoll<Site[]>("/api/sites", 15000);
  const toast = useToast();
  const { run, busy } = useAction(toast);

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

  const resolveDrift = (id: number, action: "restore" | "accept") =>
    run(() => api.post(`/api/system/drift/${id}/resolve`, { action }), action === "restore" ? "Managed config restored" : "Change accepted").then(load);

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

      {toast.node}
      {agentError && <div className="error-box"><Icon.warning /> Agent unreachable: {agentError}</div>}

      {status ? (
        <div className="grid cols-4 stagger">
          <Meter label="CPU" icon="cpu" pct={status.cpu_headroom_pct} />
          <Meter label="Memory" icon="memory" pct={status.mem_headroom_pct} />
          <Meter label="Disk" icon="disk" pct={status.disk_headroom_pct} />
          <div className="card">
            <div className="card-head"><span className="card-ico accent"><Icon.gauge /></span><h3 style={{ margin: 0 }}>Overview</h3></div>
            <div className="stat-sm">{activeSites} site{activeSites === 1 ? "" : "s"}</div>
            <div className="dim" style={{ fontSize: 13, marginTop: 6 }}>
              {status.cpu_count} cores · load {status.load1.toFixed(2)}<br />
              agent {status.agent_version}
            </div>
          </div>
        </div>
      ) : !agentError && (
        <div className="grid cols-4">
          {Array.from({ length: 4 }).map((_, i) => <div key={i} className="card skeleton skeleton-card" />)}
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
          <div className="card-list">
            {drift.map((d) => (
              <div className="task-row" key={d.id}>
                <Icon.warning />
                <span className="mono msg">{d.path}</span>
                <button className="small" disabled={busy} onClick={() => resolveDrift(d.id, "restore")}>Restore</button>
                <button className="ghost small" disabled={busy} onClick={() => resolveDrift(d.id, "accept")}>Keep change</button>
              </div>
            ))}
          </div>
        </>
      )}

      <h2>Recent activity</h2>
      <TaskFeed />

      {status && drift.length === 0 && (
        <div className="mt">
          <span className="badge good plain"><Icon.check /> No configuration drift — everything matches the panel</span>
        </div>
      )}
    </>
  );
}
