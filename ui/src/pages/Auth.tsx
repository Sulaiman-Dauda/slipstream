import { FormEvent, useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { api } from "../api";

// Auth renders either the first-boot setup form (when the panel has no
// users yet) or the login form. Setup tokens arrive via /setup/<token> URLs
// printed by the installer.
export default function Auth({ onAuthed }: { onAuthed: () => void }) {
  const location = useLocation();
  const setupToken = location.pathname.startsWith("/setup/")
    ? location.pathname.slice("/setup/".length)
    : "";

  const [needsSetup, setNeedsSetup] = useState<boolean | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [token, setToken] = useState(setupToken);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .get<{ setup_complete: boolean }>("/api/bootstrap")
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
        await api.post("/api/login", { email, password });
      }
      onAuthed();
    } catch (err) {
      setError(err instanceof Error ? err.message : "request failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="auth-shell">
      <form className="card auth-card" onSubmit={submit}>
        <h1>
          slip<span style={{ color: "var(--accent)" }}>stream</span>
        </h1>
        {needsSetup && (
          <p className="dim" style={{ textAlign: "center", marginTop: -10 }}>
            Create the administrator account to finish installation.
          </p>
        )}
        {needsSetup && (
          <>
            <label>Setup token</label>
            <input
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="from the install output"
              required
            />
          </>
        )}
        <label>Email</label>
        <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
        <label>Password</label>
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          minLength={needsSetup ? 12 : 1}
          required
        />
        {error && <div className="error-box">{error}</div>}
        <button style={{ width: "100%", marginTop: 20 }} disabled={busy}>
          {needsSetup ? "Create account" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
