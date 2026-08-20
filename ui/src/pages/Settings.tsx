import { FormEvent, useEffect, useState } from "react";
import { api } from "../api";
import { useAction, useToast } from "../components/ui";
import { Icon } from "../icons";

export default function Settings() {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [panelDomain, setPanelDomain] = useState("");

  const [attached, setAttached] = useState("");

  const loadSettings = () =>
    api.get<Record<string, string>>("/api/settings").then((s) => {
      setSettings(s);
      setAttached(s.panel_domain || "");
      setPanelDomain(s.panel_domain || "");
    }).catch(() => undefined);

  useEffect(() => { loadSettings(); }, []);

  const saveSettings = (e: FormEvent) => {
    e.preventDefault();
    run(async () => setSettings(await api.put<Record<string, string>>("/api/settings", settings)), "Settings saved");
  };
  const bind = (key: string) => ({ value: settings[key] ?? "", onChange: (e: React.ChangeEvent<HTMLInputElement>) => setSettings((s) => ({ ...s, [key]: e.target.value })) });

  const issuePanelCert = () =>
    run(() => api.post("/api/panel/certificate", { domain: panelDomain, email: settings.acme_email }),
      "Requesting certificate. The panel will restart on the new domain.")
      // Re-read so the card shows the attached domain rather than leaving the
      // operator to guess whether it took.
      .then((ok) => { if (ok) loadSettings(); });

  return (
    <>
      <h1>Settings</h1>
      <p className="sub">Panel-wide configuration. Per-site options live on each site.</p>
      {toast.node}

      <div className="card-list">
        <div className="card">
          <div className="card-head"><span className="card-ico"><Icon.globe /></span><h3 style={{ margin: 0 }}>Panel domain & HTTPS</h3></div>
          <p className="note">Point a domain at this server, then secure the panel with an automatically renewed certificate.</p>
          {attached
            ? <div className="ok-box"><Icon.check /> Panel is attached to <strong>{attached}</strong>, with a certificate that renews automatically.</div>
            : <p className="note tiny">No domain attached yet. The panel is reachable on this server&rsquo;s IP address.</p>}
          <label>Panel domain</label>
          <input value={panelDomain} onChange={(e) => setPanelDomain(e.target.value)} placeholder="panel.yourdomain.com" />
          <button className="mt" disabled={busy || !panelDomain || !settings.acme_email || panelDomain === attached} onClick={issuePanelCert}>
            {attached ? (panelDomain === attached ? "Certificate already issued" : "Move panel to this domain") : "Secure panel with Let's Encrypt"}
          </button>
          {!settings.acme_email && <p className="note tiny">Set the ACME email below first.</p>}
        </div>

        <form className="card" onSubmit={saveSettings}>
          <div className="card-head"><span className="card-ico"><Icon.lock /></span><h3 style={{ margin: 0 }}>Certificates</h3></div>
          <label>ACME account email</label>
          <input type="email" placeholder="ops@example.com" {...bind("acme_email")} />
          <p className="note tiny">Used for Let's Encrypt registration and expiry notices. Sites get certificates and auto-renewal automatically.</p>
          <button className="mt" disabled={busy}>Save</button>
        </form>

        <form className="card" onSubmit={saveSettings}>
          <div className="card-head"><span className="card-ico"><Icon.database /></span><h3 style={{ margin: 0 }}>Off-site backups (Restic)</h3></div>
          <label>Repository</label>
          <input placeholder="s3:s3.amazonaws.com/my-backups" {...bind("backup_repository")} />
          <label>Repository password</label>
          <input type="password" {...bind("backup_password")} />
          <p className="note tiny">Backups are encrypted before leaving this server and run automatically on each site's schedule. Losing this password means losing the backups.</p>
          <div className="row mt">
            <button disabled={busy}>Save</button>
            <button type="button" className="ghost" disabled={busy} onClick={() => run(async () => { const r = await api.post<{ snapshots: number }>("/api/backups/test"); toast.ok(`Repository reachable — ${r.snapshots} snapshot(s).`); })}>Test connection</button>
          </div>
        </form>

        <form className="card" onSubmit={saveSettings}>
          <div className="card-head"><span className="card-ico"><Icon.gauge /></span><h3 style={{ margin: 0 }}>Performance Guard</h3></div>
          <label>Probe target</label>
          <input placeholder="https://127.0.0.1" {...bind("probe_target")} />
          <p className="note tiny">Where Safe Push sends measurement traffic. Loopback by default so probes never leave the machine.</p>
          <button className="mt" disabled={busy}>Save</button>
        </form>

        <div className="card">
          <div className="card-head"><span className="card-ico"><Icon.download /></span><h3 style={{ margin: 0 }}>Panel updates</h3></div>
          <p className="note">Download and install the latest signed Slipstream binaries, then restart.</p>
          <label>Update URL <span className="hint">optional override</span></label>
          <input {...bind("update_url")} placeholder="https://releases.slipstream…/1.1.0" />
          <button className="ghost mt" disabled={busy} onClick={() => run(() => api.post("/api/panel/update", { base_url: settings.update_url }), "Update started")}><Icon.refresh /> Check & update</button>
        </div>
      </div>
    </>
  );
}
