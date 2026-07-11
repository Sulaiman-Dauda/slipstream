import { FormEvent, useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { api, ApiError } from "../api";

export default function Auth({ onAuthed }: { onAuthed: () => void }) {
  const location = useLocation();
  const setupToken = location.pathname.startsWith("/setup/") ? location.pathname.slice("/setup/".length) : "";

  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState(setupToken);
  const [totp, setTotp] = useState("");
  const [totpRequired, setTotpRequired] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.get<{ setup_complete: boolean }>("/api/bootstrap")
      .then((b) => setNeedsSetup(!b.setup_complete))
      .catch(() => setNeedsSetup(false));
  }, []);

  if (needsSetup === null) return null;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (needsSetup) {
        await api.post("/api/setup", { email, password, token });
      } else {
        await api.post("/api/login", { email, password, totp });
      }
      onAuthed();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401 && err.message.toLowerCase().includes("two-factor")) {
        setTotpRequired(true);
        setError(totp ? "Invalid two-factor code" : "");
      } else {
        setError(err instanceof Error ? err.message : "request failed");
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-shell">
      <form className="card auth-card pad-lg" onSubmit={submit}>
        <div className="logo"><span className="dot" /> Slipstream</div>
        {needsSetup && <p className="dim sub-center">Create the administrator account to finish installation.</p>}
        {!needsSetup && !totpRequired && <p className="dim sub-center">Sign in to your control panel.</p>}

        {needsSetup && (
          <>
            <label>Setup token</label>
            <input value={token} onChange={(e) => setToken(e.target.value)} placeholder="from the install output" required />
          </>
        )}

        {!totpRequired && (
          <>
            <label>Email</label>
            <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
            <label>Password</label>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={needsSetup ? 12 : 1} required />
          </>
        )}

        {totpRequired && (
          <>
            <p className="dim sub-center">Enter the 6-digit code from your authenticator app.</p>
            <label>Two-factor code</label>
            <input value={totp} onChange={(e) => setTotp(e.target.value)} inputMode="numeric" maxLength={6}
              placeholder="000000" autoFocus className="totp-input" />
          </>
        )}

        {error && <div className="error-box">{error}</div>}
        <button className="btn-block mt-lg" disabled={busy}>
          {busy ? <span className="spinner on-accent" /> : needsSetup ? "Create account" : totpRequired ? "Verify" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
