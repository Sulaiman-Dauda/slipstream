import { FormEvent, useState } from "react";
import { api } from "../../api";
import { Site } from "../../types";
import { useAction, useToast } from "../../components/ui";
import { Icon } from "../../icons";

export default function Migration({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [archive, setArchive] = useState("shared/migration/site.tar.gz");
  const [sql, setSQL] = useState(site.config.database.enabled ? "shared/migration/database.sql" : "");
  const [oldDomain, setOldDomain] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const confirm = prompt(`Type ${site.domain} to replace this site's current files${sql ? " and database" : ""}:`);
    if (confirm !== site.domain) return;
    run(() => api.post(`/api/sites/${site.id}/migration`, { archive, sql: sql || undefined, old_domain: oldDomain || undefined, confirm }), "Migration import started");
  };
  return (
    <div className="card">
      <div className="card-head"><span className="card-ico"><Icon.upload /></span><h3 style={{ margin: 0 }}>Import an existing site</h3></div>
      <p className="note">Upload large files through SFTP first. Slipstream rejects unsafe archive links and paths, creates an immutable release and database rollback point, then activates everything together.</p>
      <form onSubmit={submit} className="mt">
        <label>Site archive <span className="hint">.tar.gz or .tgz, relative to Files</span></label>
        <input className="mono" value={archive} onChange={(e) => setArchive(e.target.value)} required />
        {site.config.database.enabled && <><label>Database dump <span className="hint">optional .sql</span></label><input className="mono" value={sql} onChange={(e) => setSQL(e.target.value)} /></>}
        {(site.type === "wordpress" || site.type === "woocommerce") && <><label>Previous domain <span className="hint">optional; rewrites serialized WordPress URLs safely</span></label><input value={oldDomain} onChange={(e) => setOldDomain(e.target.value)} placeholder="old.example.com" /></>}
        {toast.node}
        <div className="row end mt"><button disabled={busy}>{busy ? "Starting…" : "Validate and import"}</button></div>
      </form>
    </div>
  );
}
