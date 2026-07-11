import { FormEvent, useEffect, useState } from "react";
import { api } from "../api";

export default function Settings() {
  const [settings, setSettings] = useState<Record<string, string>>({});
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    api.get<Record<string, string>>("/api/settings").then(setSettings).catch(() => undefined);
  }, []);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSaved("");
    setError("");
    try {
      setSettings(await api.put<Record<string, string>>("/api/settings", settings));
      setSaved("Settings saved.");
    } catch (err) {
      setError(err instanceof Error ? err.message : "save failed");
    }
  };

  const bind = (key: string) => ({
    value: settings[key] ?? "",
    onChange: (e: React.ChangeEvent<HTMLInputElement>) =>
      setSettings((s) => ({ ...s, [key]: e.target.value })),
  });

  return (
    <>
      <h1>Settings</h1>
      <p className="sub">Panel-wide configuration. Per-site options live on each site.</p>

      <form className="form-narrow" onSubmit={submit}>
        <div className="card">
          <h3>Certificates</h3>
          <label>ACME account email</label>
          <input type="email" placeholder="ops@example.com" {...bind("acme_email")} />
          <p className="dim" style={{ fontSize: 12 }}>
            Used for Let's Encrypt registration and expiry notices. New sites get certificates
            automatically once DNS points here.
          </p>
        </div>

        <div className="card" style={{ marginTop: 14 }}>
          <h3>Off-site backups (Restic)</h3>
          <label>Repository</label>
          <input placeholder="s3:s3.eu-central-1.amazonaws.com/my-backups" {...bind("backup_repository")} />
          <label>Repository password</label>
          <input type="password" {...bind("backup_password")} />
          <p className="dim" style={{ fontSize: 12 }}>
            Backups are encrypted before leaving this server. Losing this password means losing the
            backups — store it in a password manager.
          </p>
        </div>

        <div className="card" style={{ marginTop: 14 }}>
          <h3>Performance Guard</h3>
          <label>Probe target</label>
          <input placeholder="https://127.0.0.1" {...bind("probe_target")} />
          <p className="dim" style={{ fontSize: 12 }}>
            Where Safe Push sends its measurement traffic. Loopback by default so probes never
            leave the machine.
          </p>
        </div>

        {error && <div className="error-box">{error}</div>}
        {saved && <p style={{ color: "var(--good)" }}>{saved}</p>}
        <button style={{ marginTop: 18 }}>Save settings</button>
      </form>
    </>
  );
}
