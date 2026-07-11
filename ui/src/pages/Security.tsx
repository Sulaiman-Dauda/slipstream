import { FormEvent, useEffect, useState } from "react";
import { api, fmtAgo } from "../api";
import { useAction, useToast } from "../components/ui";

interface Me { email: string; totp_enabled: boolean }
interface SessionView { id: string; current: boolean; created_at: string; expires_at: string }

export default function Security({ me, onChange }: { me: Me; onChange: () => void }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);

  return (
    <>
      <h1>Security</h1>
      <p className="sub">Protect your control panel — it has root over this server.</p>
      {toast.node}
      <div className="grid cols-2">
        <PasswordCard run={run} busy={busy} />
        <TwoFactorCard me={me} run={run} busy={busy} onChange={onChange} toast={toast} />
      </div>
      <h2>Active sessions</h2>
      <Sessions run={run} />
    </>
  );
}

type RunFn = (fn: () => Promise<unknown>, ok?: string) => Promise<boolean>;

function PasswordCard({ run, busy }: { run: RunFn; busy: boolean }) {
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const submit = (e: FormEvent) => { e.preventDefault(); run(() => api.post("/api/account/password", { current_password: cur, new_password: next }), "Password changed").then((ok) => { if (ok) { setCur(""); setNext(""); } }); };
  return (
    <form className="card" onSubmit={submit}>
      <h3>Change password</h3>
      <label>Current password</label>
      <input type="password" value={cur} onChange={(e) => setCur(e.target.value)} required />
      <label>New password <span className="hint">12+ characters</span></label>
      <input type="password" value={next} onChange={(e) => setNext(e.target.value)} minLength={12} required />
      <button className="mt" disabled={busy}>Update password</button>
    </form>
  );
}

function TwoFactorCard({ me, run, busy, onChange, toast }: { me: Me; run: RunFn; busy: boolean; onChange: () => void; toast: ReturnType<typeof useToast> }) {
  const [enroll, setEnroll] = useState<{ secret: string; qr_data: string } | null>(null);
  const [code, setCode] = useState("");
  const [disablePw, setDisablePw] = useState("");

  const begin = () => run(async () => setEnroll(await api.post("/api/account/2fa/begin")));
  const confirm = () => run(() => api.post("/api/account/2fa/confirm", { code }), "Two-factor enabled").then((ok) => { if (ok) { setEnroll(null); setCode(""); onChange(); } });
  const disable = () => run(() => api.post("/api/account/2fa/disable", { password: disablePw }), "Two-factor disabled").then((ok) => { if (ok) { setDisablePw(""); onChange(); } });

  return (
    <div className="card">
      <h3>Two-factor authentication</h3>
      {me.totp_enabled ? (
        <>
          <div className="ok-box" style={{ marginTop: 8 }}>2FA is active on your account.</div>
          <label>Disable — confirm with your password</label>
          <input type="password" value={disablePw} onChange={(e) => setDisablePw(e.target.value)} />
          <button className="danger mt" disabled={busy || !disablePw} onClick={disable}>Disable 2FA</button>
        </>
      ) : enroll ? (
        <>
          <p className="dim" style={{ fontSize: 13 }}>Scan with Google Authenticator, 1Password, or Authy — then enter the 6-digit code.</p>
          <div style={{ textAlign: "center", margin: "12px 0" }}><img src={enroll.qr_data} alt="2FA QR code" style={{ width: 180, borderRadius: 8, background: "#fff", padding: 8 }} /></div>
          <p className="dim3 mono" style={{ fontSize: 12, textAlign: "center", wordBreak: "break-all" }}>{enroll.secret}</p>
          <label>Verification code</label>
          <input value={code} onChange={(e) => setCode(e.target.value)} maxLength={6} inputMode="numeric" placeholder="000000" style={{ textAlign: "center", letterSpacing: "0.3em" }} />
          <button className="mt" disabled={busy || code.length !== 6} onClick={confirm}>Verify & enable</button>
        </>
      ) : (
        <>
          <p className="dim" style={{ fontSize: 13 }}>Add a second factor from an authenticator app. Strongly recommended.</p>
          <button className="mt" disabled={busy} onClick={begin}>Set up 2FA</button>
        </>
      )}
      {toast.node}
    </div>
  );
}

function Sessions({ run }: { run: RunFn }) {
  const [sessions, setSessions] = useState<SessionView[]>([]);
  const load = () => api.get<SessionView[]>("/api/account/sessions").then((s) => setSessions(s || [])).catch(() => undefined);
  useEffect(() => { load(); }, []);
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Session</th><th>Started</th><th>Expires</th><th></th></tr></thead>
        <tbody>
          {sessions.map((s) => (
            <tr key={s.id}>
              <td className="mono">{s.id}… {s.current && <span className="badge accent plain" style={{ marginLeft: 6 }}>this device</span>}</td>
              <td className="dim">{fmtAgo(s.created_at)}</td>
              <td className="dim">{new Date(s.expires_at).toLocaleString()}</td>
              <td>{!s.current && <button className="danger tiny" onClick={() => run(() => api.del(`/api/account/sessions/${s.id}`), "Session revoked").then(load)}>Revoke</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
