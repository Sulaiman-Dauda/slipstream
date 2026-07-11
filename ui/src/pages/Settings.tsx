import { FormEvent, useEffect, useState } from "react";
import { api } from "../api";
import { useAction, useToast } from "../components/ui";

export default function Settings() {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [panelDomain, setPanelDomain] = useState("");

  useEffect(() => {
    api.get<Record<string, string>>("/api/settings").then((s) => { setSettings(s); setPanelDomain(s.panel_domain || ""); }).catch(() => undefined);
  }, []);

  const saveSettings = (e: FormEvent) => {
    e.preventDefault();
    run(async () => setSettings(await api.put<Record<string, string>>("/api/settings", settings)), "Settings saved");
  };
  const bind = (key: string) => ({ value: settings[key] ?? "", onChange: (e: React.ChangeEvent<HTMLInputElement>) => setSettings((s) => ({ ...s, [key]: e.target.value })) });

  const issuePanelCert = () =>
    run(() => api.post("/api/panel/certificate", { domain: panelDomain, email: settings.acme_email }),
      "Requesting certificate — the panel will restart on the new domain");

  return (
    <>
      <h1>Settings</h1>
      <p className="sub">Panel-wide configuration. Per-site options live on each site.</p>
      {toast.node}

      <div className="card-list">
        <div className="card">
          <h3>Panel domain & HTTPS</h3>
          <p className="dim" style={{ fontSize: 13 }}>Point a domain at this server, then get a real certificate for the panel itself. Runs on port 5252.</p>
          <label>Panel domain</label>
          <input value={panelDomain} onChange={(e) => setPanelDomain(e.target.value)} placeholder="panel.yourdomain.com" />
          <button className="mt" disabled={busy || !panelDomain || !settings.acme_email} onClick={issuePanelCert}>Secure panel with Let's Encrypt</button>
          {!settings.acme_email && <p className="dim3" style={{ fontSize: 12, marginTop: 8 }}>Set the ACME email below first.</p>}
        </div>

        <form className="card" onSubmit={saveSettings}>
          <h3>Certificates</h3>
          <label>ACME account email</label>
          <input type="email" placeholder="ops@example.com" {...bind("acme_email")} />
          <p className="dim3" style={{ fontSize: 12, marginTop: 8 }}>Used for Let's Encrypt registration and expiry notices. Sites get certificates and auto-renewal automatically.</p>
          <button className="mt" disabled={busy}>Save</button>
        </form>

        <form className="card" onSubmit={saveSettings}>
          <h3>Off-site backups (Restic)</h3>
          <label>Repository</label>
          <input placeholder="s3:s3.amazonaws.com/my-backups" {...bind("backup_repository")} />
          <label>Repository password</label>
          <input type="password" {...bind("backup_password")} />
          <p className="dim3" style={{ fontSize: 12, marginTop: 8 }}>Backups are encrypted before leaving this server and run automatically on each site's schedule. Losing this password means losing the backups.</p>
          <button className="mt" disabled={busy}>Save</button>
        </form>

        <form className="card" onSubmit={saveSettings}>
          <h3>Performance Guard</h3>
          <label>Probe target</label>
          <input placeholder="https://127.0.0.1" {...bind("probe_target")} />
          <p className="dim3" style={{ fontSize: 12, marginTop: 8 }}>Where Safe Push sends measurement traffic. Loopback by default so probes never leave the machine.</p>
          <button className="mt" disabled={busy}>Save</button>
        </form>

        <div className="card">
          <h3>Panel updates</h3>
          <p className="dim" style={{ fontSize: 13 }}>Download and install the latest signed Slipstream binaries, then restart.</p>
          <label>Update URL <span className="hint">optional override</span></label>
          <input {...bind("update_url")} placeholder="https://releases.slipstream…/1.1.0" />
          <button className="ghost mt" disabled={busy} onClick={() => run(() => api.post("/api/panel/update", { base_url: settings.update_url }), "Update started")}>Check & update</button>
        </div>
      </div>
    </>
  );
}
