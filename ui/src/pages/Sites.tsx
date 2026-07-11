import { FormEvent, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { Site, SiteType } from "../types";
import { Modal, StatusBadge, usePoll, useToast } from "../components/ui";
import { Icon } from "../icons";

const typeLabels: Record<SiteType, string> = {
  wordpress: "WordPress", woocommerce: "WooCommerce", static: "Static",
  php: "PHP", laravel: "Laravel", proxy: "Reverse proxy",
};
const typeIcon: Record<SiteType, string> = {
  wordpress: "◆", woocommerce: "🛒", static: "▤", php: "⌘", laravel: "▲", proxy: "⇄",
};

// orderSites lays production sites out with their staging environment
// directly beneath, so the connector wire ties the two together visually.
function orderSites(sites: Site[]): { site: Site; isStaging: boolean; hasStaging: boolean }[] {
  const stagingByProd = new Map<number, Site[]>();
  for (const s of sites) {
    if (s.staging_of) {
      const arr = stagingByProd.get(s.staging_of) || [];
      arr.push(s);
      stagingByProd.set(s.staging_of, arr);
    }
  }
  const out: { site: Site; isStaging: boolean; hasStaging: boolean }[] = [];
  for (const s of sites) {
    if (s.staging_of) continue; // placed under its production site
    const children = stagingByProd.get(s.id) || [];
    out.push({ site: s, isStaging: false, hasStaging: children.length > 0 });
    for (const c of children) out.push({ site: c, isStaging: true, hasStaging: false });
  }
  // Orphan staging sites (production removed) still get listed.
  for (const s of sites) {
    if (s.staging_of && !sites.some((p) => p.id === s.staging_of)) {
      out.push({ site: s, isStaging: true, hasStaging: false });
    }
  }
  return out;
}

export default function Sites() {
  const [sites] = usePoll<Site[]>("/api/sites", 8000);
  const [creating, setCreating] = useState(false);

  return (
    <>
      <div className="topbar">
        <div>
          <h1>Sites</h1>
          <p className="sub">Every site is isolated, cached, and backed up by default.</p>
        </div>
        <button onClick={() => setCreating(true)}><Icon.plus /> New site</button>
      </div>

      {sites === null ? (
        <div className="empty"><span className="spinner" /></div>
      ) : sites.length === 0 ? (
        <div className="card empty">
          <div className="big">◫</div>
          <div className="title">No sites yet</div>
          <p>Launch a production-ready WordPress site in one click.</p>
          <button onClick={() => setCreating(true)} style={{ marginTop: 14 }}><Icon.plus /> Create your first site</button>
        </div>
      ) : (
        <div className="site-list">
          {orderSites(sites).map(({ site: s, isStaging, hasStaging }) => (
            <Link
              className={"site-row" + (isStaging ? " is-staging" : "") + (hasStaging ? " has-staging" : "")}
              to={`/sites/${s.id}`} key={s.id}
            >
              <span className="favicon">{typeIcon[s.type]}</span>
              <div className="rowmain">
                <div className="domain">
                  <span>{s.domain}</span>
                  {isStaging && <span className="badge staging plain">staging</span>}
                </div>
                <div className="meta">
                  <span>{typeLabels[s.type]}</span><span className="pip" />
                  <span>{s.profile}</span>
                  {s.php_version && <><span className="pip" /><span>PHP {s.php_version}</span></>}
                  <span className="pip" /><span className="cachestate">{s.config.cache_enabled ? "Cache on" : "Cache off"}</span>
                </div>
              </div>
              <div className="rowend">
                <StatusBadge status={s.status} />
              </div>
            </Link>
          ))}
        </div>
      )}

      {creating && <CreateSiteModal onClose={() => setCreating(false)} />}
    </>
  );
}

function CreateSiteModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [domain, setDomain] = useState("");
  const [type, setType] = useState<SiteType>("wordpress");
  const [profile, setProfile] = useState("");
  const [title, setTitle] = useState("");
  const [adminEmail, setAdminEmail] = useState("");
  const [adminUser, setAdminUser] = useState("");
  const [adminPassword, setAdminPassword] = useState("");
  const [proxyUpstream, setProxyUpstream] = useState("");
  const [busy, setBusy] = useState(false);
  const isWP = type === "wordpress" || type === "woocommerce";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const payload: Record<string, unknown> = { domain, type };
      if (profile) payload.profile = profile;
      if (isWP) { payload.title = title; payload.admin_email = adminEmail; payload.admin_user = adminUser; payload.admin_password = adminPassword; }
      if (type === "proxy") payload.proxy_upstream = proxyUpstream;
      await api.post("/api/sites", payload);
      onClose();
    } catch (err) {
      toast.err(err instanceof Error ? err.message : "creation failed");
      setBusy(false);
    }
  };

  return (
    <Modal title="New site" onClose={onClose}>
      <form onSubmit={submit}>
        <label>Domain</label>
        <input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="example.com" required autoFocus />
        <label>Type</label>
        <select value={type} onChange={(e) => setType(e.target.value as SiteType)}>
          {Object.entries(typeLabels).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </select>
        <label>Performance profile</label>
        <select value={profile} onChange={(e) => setProfile(e.target.value)}>
          <option value="">Recommended ({type === "woocommerce" ? "commerce" : "balanced"})</option>
          <option value="balanced">Balanced — safe caching for most sites</option>
          <option value="commerce">Commerce — strict bypass for carts & checkout</option>
          <option value="maximum">Maximum — aggressive caching for publications</option>
        </select>
        {isWP && (
          <>
            <label>Site title</label>
            <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="My Store" />
            <div className="field-row">
              <div><label>Admin email</label><input type="email" value={adminEmail} onChange={(e) => setAdminEmail(e.target.value)} required /></div>
              <div><label>Admin username</label><input value={adminUser} onChange={(e) => setAdminUser(e.target.value)} required /></div>
            </div>
            <label>Admin password <span className="hint">12+ characters</span></label>
            <input type="password" value={adminPassword} onChange={(e) => setAdminPassword(e.target.value)} minLength={12} required />
          </>
        )}
        {type === "proxy" && (
          <>
            <label>Upstream URL</label>
            <input value={proxyUpstream} onChange={(e) => setProxyUpstream(e.target.value)} placeholder="http://127.0.0.1:3000" required />
          </>
        )}
        {toast.node}
        <div className="row end mt-lg">
          <button type="button" className="ghost" onClick={onClose}>Cancel</button>
          <button disabled={busy}>{busy ? "Creating…" : "Create site"}</button>
        </div>
      </form>
    </Modal>
  );
}
