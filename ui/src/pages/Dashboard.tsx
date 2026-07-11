import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { DriftEvent, SystemStatus } from "../types";
import TaskFeed from "../components/TaskFeed";

function Meter({ label, pct, invert }: { label: string; pct: number; invert?: boolean }) {
  // pct is headroom: high = good. invert flips for "usage" style metrics.
  const shown = invert ? 100 - pct : pct;
  const cls = pct < 15 ? "bad" : pct < 35 ? "warn" : "";
  return (
    <div className="card">
      <h3>{label}</h3>
      <div className="stat">
        {shown}
        <small>%</small>
      </div>
      <div className={`meter ${cls}`}>
        <div style={{ width: `${Math.max(2, Math.min(100, shown))}%` }} />
      </div>
    </div>
  );
}

function ServiceBadge({ name, up }: { name: string; up: boolean }) {
  return (
    <span className={`badge ${up ? "good" : "bad"}`} style={{ marginRight: 8 }}>
      {name} {up ? "running" : "down"}
    </span>
  );
}

export default function Dashboard() {
  const [status, setStatus] = useState<SystemStatus | null>(null);
  const [drift, setDrift] = useState<DriftEvent[]>([]);
  const [agentError, setAgentError] = useState("");

  const load = useCallback(() => {
    api
      .get<SystemStatus>("/api/system/status")
      .then((s) => {
        setStatus(s);
        setAgentError("");
      })
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

  return (
    <>
      <h1>Server capacity</h1>
      <p className="sub">Headroom left before this machine becomes the bottleneck.</p>

      {agentError && <div className="error-box">Agent unreachable: {agentError}</div>}

      {status && (
        <>
          <div className="grid cols-3">
            <Meter label="CPU headroom" pct={status.cpu_headroom_pct} />
            <Meter label="Memory headroom" pct={status.mem_headroom_pct} />
            <Meter label="Disk headroom" pct={status.disk_headroom_pct} />
          </div>
          <div style={{ marginTop: 16 }}>
            <ServiceBadge name="nginx" up={status.nginx_running} />
            <ServiceBadge name="mariadb" up={status.mariadb_running} />
            {status.redis_running && <ServiceBadge name="redis" up />}
            <span className="dim" style={{ fontSize: 12 }}>
              {status.cpu_count} cores · load {status.load1.toFixed(2)} · agent {status.agent_version}
            </span>
          </div>
        </>
      )}

      {drift.length > 0 && (
        <>
          <h2>Configuration drift</h2>
          <p className="sub">
            These managed files were changed outside the panel. Nothing has been overwritten.
          </p>
          {drift.map((d) => (
            <div className="task-row" key={d.id} style={{ marginBottom: 8 }}>
              <span className="mono msg">{d.path}</span>
              <button className="small" onClick={() => resolveDrift(d.id, "restore")}>
                Restore managed config
              </button>
              <button className="ghost small" onClick={() => resolveDrift(d.id, "accept")}>
                Keep manual change
              </button>
            </div>
          ))}
        </>
      )}

      <h2>Recent activity</h2>
      <TaskFeed />
    </>
  );
}
