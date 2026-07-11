import { api } from "../api";
import { ServiceInfo } from "../types";
import { useAction, usePoll, useToast } from "../components/ui";

export default function Services() {
  const [services, reload] = usePoll<ServiceInfo[]>("/api/services", 10000);
  const toast = useToast();
  const { run, busy } = useAction(toast);

  return (
    <>
      <h1>Services</h1>
      <p className="sub">The system services powering your sites.</p>
      {toast.node}
      <div className="grid cols-2">
        {(services || []).map((s) => (
          <div className="card" key={s.name}>
            <div className="row between">
              <div>
                <div style={{ fontWeight: 650, fontSize: 15 }}>{s.name}</div>
                <div className="dim3 mono" style={{ fontSize: 12 }}>{s.unit}</div>
              </div>
              <span className={`badge ${s.active ? "good" : "bad"}`}>{s.active ? "running" : "stopped"}</span>
            </div>
            <div className="row between mt">
              <span className="dim3" style={{ fontSize: 12 }}>{s.enabled ? "starts on boot" : "manual start"}</span>
              <button className="ghost small" disabled={busy} onClick={() => run(() => api.post(`/api/services/${s.name}/restart`), `${s.name} restarted`).then(reload)}>Restart</button>
            </div>
          </div>
        ))}
        {(!services || services.length === 0) && <div className="empty"><span className="spinner" /></div>}
      </div>
    </>
  );
}
