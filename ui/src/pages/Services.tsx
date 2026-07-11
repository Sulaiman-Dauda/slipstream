import { api } from "../api";
import { ServiceInfo } from "../types";
import { Skeleton, useAction, usePoll, useToast } from "../components/ui";
import { Icon } from "../icons";

function serviceIcon(name: string): keyof typeof Icon {
  const n = name.toLowerCase();
  if (n.includes("nginx") || n.includes("caddy")) return "globe";
  if (n.includes("php")) return "code";
  if (n.includes("maria") || n.includes("mysql")) return "database";
  if (n.includes("redis")) return "layers";
  return "server";
}

export default function Services() {
  const { data: services, error, reload } = usePoll<ServiceInfo[]>("/api/services", 10000);
  const toast = useToast();
  const { run, busy } = useAction(toast);

  return (
    <>
      <h1>Services</h1>
      <p className="sub">The system services powering your sites.</p>
      {toast.node}
      {error && <div className="error-box"><Icon.warning /> {error}</div>}
      {!services ? (!error && <Skeleton count={4} />) : (
        <div className="card-list stagger">
          {services.map((s) => {
            const SvcIcon = Icon[serviceIcon(s.name)];
            return (
              <div className="card" key={s.name}>
                <div className="row between">
                  <div className="card-head" style={{ marginBottom: 0 }}>
                    <span className={`card-ico ${s.active ? "good" : "bad"}`}><SvcIcon /></span>
                    <div>
                      <div style={{ fontWeight: 650, fontSize: 15 }}>{s.name}</div>
                      <div className="dim3 mono" style={{ fontSize: 12 }}>{s.unit}</div>
                    </div>
                  </div>
                  <span className={`badge ${s.active ? "good" : "bad"}`}>{s.active ? "running" : "stopped"}</span>
                </div>
                <div className="row between mt">
                  <span className="dim3" style={{ fontSize: 12 }}>{s.enabled ? "starts on boot" : "manual start"}</span>
                  <button className="ghost small" disabled={busy} onClick={() => run(() => api.post(`/api/services/${s.name}/restart`), `${s.name} restarted`).then(reload)}><Icon.refresh /> Restart</button>
                </div>
              </div>
            );
          })}
          {services.length === 0 && <div className="empty">No services reported.</div>}
        </div>
      )}
    </>
  );
}
