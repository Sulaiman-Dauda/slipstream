import { FormEvent, useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { Site, SiteType } from "../types";

const typeLabels: Record<SiteType, string> = {
  wordpress: "WordPress",
  woocommerce: "WooCommerce",
  static: "Static",
  php: "PHP",
  laravel: "Laravel",
  proxy: "Reverse proxy",
};

const statusBadge: Record<Site["status"], string> = {
  provisioning: "accent",
  active: "good",
  error: "bad",
  deleting: "warn",
};

export default function Sites() {
  const [sites, setSites] = useState<Site[]>([]);
  const [creating, setCreating] = useState(false);

  const load = useCallback(() => {
    api.get<Site[]>("/api/sites").then((s) => setSites(s ?? [])).catch(() => undefined);
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 8000);
    return () => clearInterval(t);
  }, [load]);

  return (
    <>
      <div className="row between">
        <div>
          <h1>Sites</h1>
          <p className="sub">Every site is isolated, cached, and backed up by default.</p>
        </div>
        <button onClick={() => setCreating(true)}>+ New site</button>
      </div>

      {sites.length === 0 ? (
        <div className="card" style={{ textAlign: "center", padding: 48 }}>
          <p style={{ fontSize: 16, fontWeight: 600 }}>No sites yet</p>
          <p className="dim">Launch a production-ready WordPress site in one click.</p>
          <button onClick={() => setCreating(true)}>Create your first site</button>
        </div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Domain</th>
              <th>Type</th>
              <th>Profile</th>
              <th>PHP</th>
              <th>Cache</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {sites.map((s) => (
              <tr key={s.id}>
                <td>
                  <Link to={`/sites/${s.id}`}>{s.domain}</Link>
                  {s.staging_of ? <span className="badge dim" style={{ marginLeft: 8 }}>staging</span> : null}
                </td>
                <td>{typeLabels[s.type]}</td>
                <td>{s.profile}</td>
                <td>{s.php_version || "—"}</td>
                <td>{s.config.cache_enabled ? <span className="badge good">on</span> : <span className="badge dim">off</span>}</td>
                <td>
                  <span className={`badge ${statusBadge[s.status]}`}>{s.status}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating && <CreateSiteModal onClose={() => { setCreating(false); load(); }} />}
    </>
  );
}

function CreateSiteModal({ onClose }: { onClose: () => void }) {
  const [domain, setDomain] = useState("");
  const [type, setType] = useState<SiteType>("wordpress");
  const [profile, setProfile] = useState("");
  const [title, setTitle] = useState("");
  const [adminEmail, setAdminEmail] = useState("");
  const [adminUser, setAdminUser] = useState("");
  const [adminPassword, setAdminPassword] = useState("");
  const [proxyUpstream, setProxyUpstream] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const isWP = type === "wordpress" || type === "woocommerce";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const payload: Record<string, unknown> = { domain, type };
      if (profile) payload.profile = profile;
      if (isWP) {
        payload.title = title;
        payload.admin_email = adminEmail;
        payload.admin_user = adminUser;
        payload.admin_password = adminPassword;
      }
      if (type === "proxy") payload.proxy_upstream = proxyUpstream;
      await api.post("/api/sites", payload);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "creation failed");
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <form className="card modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <h2 style={{ marginTop: 0 }}>New site</h2>

        <label>Domain</label>
        <input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="example.com" required />

        <label>Type</label>
        <select value={type} onChange={(e) => setType(e.target.value as SiteType)}>
          {Object.entries(typeLabels).map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>

        <label>Performance profile</label>
        <select value={profile} onChange={(e) => setProfile(e.target.value)}>
          <option value="">Recommended ({type === "woocommerce" ? "commerce" : "balanced"})</option>
          <option value="balanced">Balanced — safe caching for most sites</option>
          <option value="commerce">Commerce — strict bypass for carts and checkout</option>
          <option value="maximum">Maximum — aggressive caching for publications</option>
        </select>

        {isWP && (
          <>
            <label>Site title</label>
            <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="My Store" />
            <label>Admin email</label>
            <input type="email" value={adminEmail} onChange={(e) => setAdminEmail(e.target.value)} required />
            <label>Admin username</label>
            <input value={adminUser} onChange={(e) => setAdminUser(e.target.value)} required />
            <label>Admin password (12+ characters)</label>
            <input
              type="password"
              value={adminPassword}
              onChange={(e) => setAdminPassword(e.target.value)}
              minLength={12}
              required
            />
          </>
        )}

        {type === "proxy" && (
          <>
            <label>Upstream URL</label>
            <input
              value={proxyUpstream}
              onChange={(e) => setProxyUpstream(e.target.value)}
              placeholder="http://127.0.0.1:3000"
              required
            />
          </>
        )}

        {error && <div className="error-box">{error}</div>}

        <div className="row" style={{ marginTop: 22, justifyContent: "flex-end" }}>
          <button type="button" className="ghost" onClick={onClose}>
            Cancel
          </button>
          <button disabled={busy}>{busy ? "Creating…" : "Create site"}</button>
        </div>
      </form>
    </div>
  );
}
