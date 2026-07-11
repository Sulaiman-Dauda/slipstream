import { FormEvent, useState } from "react";
import { api } from "../api";
import { useAction, usePoll, useToast } from "../components/ui";

interface FWStatus { enabled: boolean; rules: string[] }

export default function Firewall() {
  const [status, reload] = usePoll<FWStatus>("/api/firewall", 0);
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
          <h3>Status <span className={`badge ${status?.enabled ? "good" : "warn"}`} style={{ marginLeft: 8 }}>{status?.enabled ? "active" : "inactive"}</span></h3>
          <div className="mono" style={{ fontSize: 12.5, marginTop: 12, whiteSpace: "pre-wrap" }}>
            {status?.rules?.length ? status.rules.join("\n") : "No rules."}
          </div>
        </div>
        <form className="card" onSubmit={submit}>
          <h3>Add / remove rule</h3>
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
          <p className="dim3" style={{ fontSize: 12, marginTop: 10 }}>Tip: keep 22 (SSH), 80, 443 and 5252 (panel) open. To lock the panel to your office, allow 5252 from your IP and delete the open 5252 rule.</p>
        </form>
      </div>
    </>
  );
}
