import { FormEvent, useState } from "react";
import { api } from "../api";
import { useAction, usePoll, useToast } from "../components/ui";
import { Icon } from "../icons";

interface FWStatus { enabled: boolean; rules: string[] }

export default function Firewall() {
  const { data: status, reload } = usePoll<FWStatus>("/api/firewall", 0);
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [port, setPort] = useState("");
  const [action, setAction] = useState("allow");
  const [from, setFrom] = useState("");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    run(() => api.post("/api/firewall/rule", { action, port: Number(port), proto: "tcp", from: from || undefined }), "Firewall rule applied")
      .then((ok) => { if (ok) { setPort(""); setFrom(""); reload(); } });
  };

  return (
    <>
      <h1>Firewall</h1>
      <p className="sub">Control which ports are reachable. UFW under the hood.</p>
      {toast.node}

      <div className="card-list">
        <div className="card">
          <div className="card-head">
            <span className={`card-ico ${status?.enabled ? "good" : "warn"}`}><Icon.shield /></span>
            <h3 style={{ margin: 0 }}>Status</h3>
            <span className={`badge end ${status?.enabled ? "good" : "warn"}`}>{status?.enabled ? "active" : "inactive"}</span>
          </div>
          <div className="log-window mt">
            <div className="log-window-head"><span className="dots"><span /><span /><span /></span><span className="path">ufw status</span></div>
            <pre className="log">{status?.rules?.length ? status.rules.join("\n") : "No rules."}</pre>
          </div>
        </div>
        <form className="card" onSubmit={submit}>
          <div className="card-head"><span className="card-ico"><Icon.key /></span><h3 style={{ margin: 0 }}>Add / remove rule</h3></div>
          <label>Action</label>
          <select value={action} onChange={(e) => setAction(e.target.value)}>
            <option value="allow">Allow port</option>
            <option value="deny">Deny port</option>
            <option value="delete">Delete allow rule</option>
          </select>
          <label>Port</label>
          <input type="number" value={port} onChange={(e) => setPort(e.target.value)} placeholder="e.g. 8080" required />
          <label>Restrict to source IP <span className="hint">optional — e.g. lock the panel to your IP</span></label>
          <input value={from} onChange={(e) => setFrom(e.target.value)} placeholder="203.0.113.4" />
          <button className="mt" disabled={busy}>Apply rule</button>
          <p className="note tiny">Keep 22 (SSH), 80 and 443 open. For tighter security, restrict SSH and panel access to trusted source IPs.</p>
        </form>
      </div>
    </>
  );
}
