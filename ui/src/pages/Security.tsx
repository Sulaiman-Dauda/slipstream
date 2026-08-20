import { FormEvent, useEffect, useState } from "react";
import { api, fmtAgo } from "../api";
import { useAction, useToast } from "../components/ui";
import { Icon } from "../icons";

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
      <div className="card-list">
        <PasswordCard run={run} busy={busy} />
        <TwoFactorCard me={me} run={run} busy={busy} onChange={onChange} />
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
  const [confirm, setConfirm] = useState("");
  const [reveal, setReveal] = useState(false);

  // Typed far enough to judge, so saying so now beats failing on submit.
  const mismatch = confirm !== "" && next !== confirm;
  const tooShort = next !== "" && next.length < 12;
  const ready = cur !== "" && next.length >= 12 && next === confirm;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!ready) return;
    run(() => api.post("/api/account/password", { current_password: cur, new_password: next }), "Password changed")
      .then((ok) => { if (ok) { setCur(""); setNext(""); setConfirm(""); setReveal(false); } });
  };

  const type = reveal ? "text" : "password";
  return (
    <form className="card" onSubmit={submit}>
      <div className="card-head">
        <span className="card-ico"><Icon.lock /></span>
        <h3 style={{ margin: 0 }}>Change password</h3>
        <button type="button" className="reveal-toggle end" onClick={() => setReveal((v) => !v)}
          aria-pressed={reveal} title={reveal ? "Hide passwords" : "Show passwords"}>
          {reveal ? <Icon.eyeOff /> : <Icon.eye />}{reveal ? "Hide" : "Show"}
        </button>
      </div>
      <label>Current password</label>
      <input type={type} value={cur} onChange={(e) => setCur(e.target.value)} autoComplete="current-password" required />
      <label>New password <span className="hint">12+ characters</span></label>
      <input type={type} value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" minLength={12} required />
      {tooShort && <p className="note tiny warn">{12 - next.length} more character{12 - next.length === 1 ? "" : "s"} needed.</p>}
      <label>Confirm new password</label>
      <input type={type} value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
      {mismatch && <p className="note tiny warn">Both new password fields must match.</p>}
      <button className="mt" disabled={busy || !ready}>Update password</button>
    </form>
  );
}

function TwoFactorCard({ me, run, busy, onChange }: { me: Me; run: RunFn; busy: boolean; onChange: () => void }) {
  const [enroll, setEnroll] = useState<{ secret: string; qr_data: string } | null>(null);
  const [code, setCode] = useState("");
  const [disablePw, setDisablePw] = useState("");

  const begin = () => run(async () => setEnroll(await api.post("/api/account/2fa/begin")));
  const confirm = () => run(() => api.post("/api/account/2fa/confirm", { code }), "Two-factor enabled").then((ok) => { if (ok) { setEnroll(null); setCode(""); onChange(); } });
  const disable = () => run(() => api.post("/api/account/2fa/disable", { password: disablePw }), "Two-factor disabled").then((ok) => { if (ok) { setDisablePw(""); onChange(); } });

  return (
    <div className="card">
      <div className="card-head">
        <span className={`card-ico ${me.totp_enabled ? "good" : ""}`}><Icon.shield /></span>
        <h3 style={{ margin: 0 }}>Two-factor authentication</h3>
        {me.totp_enabled && <span className="badge good plain end">enabled</span>}
      </div>
      {me.totp_enabled ? (
        <>
          <div className="ok-box" style={{ marginTop: 8 }}><Icon.check /> 2FA is active on your account.</div>
          <label>Disable — confirm with your password</label>
          <input type="password" value={disablePw} onChange={(e) => setDisablePw(e.target.value)} />
          <button className="danger mt" disabled={busy || !disablePw} onClick={disable}>Disable 2FA</button>
        </>
      ) : enroll ? (
        <>
          <p className="note">Scan with Google Authenticator, 1Password, or Authy — then enter the 6-digit code.</p>
          <div className="qr-frame"><img src={enroll.qr_data} alt="2FA QR code" /></div>
          <p className="dim3 mono" style={{ fontSize: 12, textAlign: "center", wordBreak: "break-all" }}>{enroll.secret}</p>
          <label>Verification code</label>
          <input value={code} onChange={(e) => setCode(e.target.value)} maxLength={6} inputMode="numeric" placeholder="000000" className="totp-input" />
          <button className="mt" disabled={busy || code.length !== 6} onClick={confirm}>Verify & enable</button>
        </>
      ) : (
        <>
          <p className="note">Add a second factor from an authenticator app. Strongly recommended.</p>
          <button className="mt" disabled={busy} onClick={begin}>Set up 2FA</button>
        </>
      )}
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
