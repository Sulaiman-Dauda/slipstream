import { useCallback, useEffect, useState } from "react";
import { api, fmtBytes } from "../../api";
import { FileEntry, Site } from "../../types";
import { Modal, useAction, useToast } from "../../components/ui";
import { Icon } from "../../icons";

export default function Files({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [editing, setEditing] = useState<{ path: string; content: string } | null>(null);

  const load = useCallback((p: string) => {
    api.get<{ entries: FileEntry[] }>(`/api/sites/${site.id}/files?path=${encodeURIComponent(p)}`)
      .then((r) => { setEntries(r.entries || []); setPath(p); })
      .catch((e) => toast.err(e instanceof Error ? e.message : "cannot read directory"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [site.id]);

  useEffect(() => { load(""); }, [load]);

  const open = (e: FileEntry) => {
    const next = path ? `${path}/${e.name}` : e.name;
    if (e.is_dir) { load(next); return; }
    if (e.size > 2 * 1024 * 1024) { toast.err("File too large to edit here"); return; }
    api.get<{ content: string; truncated: boolean }>(`/api/sites/${site.id}/files/read?path=${encodeURIComponent(next)}`)
      .then((r) => setEditing({ path: next, content: r.content }))
      .catch((err) => toast.err(err instanceof Error ? err.message : "cannot open file"));
  };

  const up = () => { const parts = path.split("/").filter(Boolean); parts.pop(); load(parts.join("/")); };
  const save = () => editing && run(() => api.post(`/api/sites/${site.id}/files/write`, { path: editing.path, content: editing.content }), "Saved").then((ok) => ok && setEditing(null));

  return (
    <>
      <div className="crumb">
        <a onClick={() => load("")} className="crumb-link">{site.domain}</a>
        {path && " / " + path}
      </div>
      <div className="table-wrap">
        {path && <div className="filerow" onClick={up}><span className="ico"><Icon.arrowUp /></span> <span className="dim">..</span></div>}
        {entries.map((e) => (
          <div className="filerow" key={e.name} onClick={() => open(e)}>
            <span className="ico">{e.is_dir ? <Icon.folder /> : <Icon.file />}</span>
            <span style={{ flex: 1 }}>{e.name}</span>
            <span className="dim3 mono" style={{ fontSize: 12 }}>{e.mode}</span>
            <span className="dim3" style={{ fontSize: 12, width: 80, textAlign: "right" }}>{e.is_dir ? "" : fmtBytes(e.size)}</span>
          </div>
        ))}
        {entries.length === 0 && <div className="filerow dim">Empty directory</div>}
      </div>
      <p className="note tiny">
        For uploads and bulk transfers, enable SFTP in the SFTP tab and connect with any client.
      </p>

      {editing && (
        <Modal title={editing.path} onClose={() => setEditing(null)} wide>
          <textarea value={editing.content} onChange={(e) => setEditing({ ...editing, content: e.target.value })} rows={20} />
          {toast.node}
          <div className="row end mt">
            <button className="ghost" onClick={() => setEditing(null)}>Cancel</button>
            <button onClick={save} disabled={busy}>{busy ? "Saving…" : "Save"}</button>
          </div>
        </Modal>
      )}
    </>
  );
}
