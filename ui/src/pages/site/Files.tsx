import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, fmtBytes } from "../../api";
import { FileEntry, Site } from "../../types";
import { Modal, useAction, useToast } from "../../components/ui";
import { Icon } from "../../icons";

export default function Files({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [editing, setEditing] = useState<{ path: string; content: string } | null>(null);
  const uploadInput = useRef<HTMLInputElement>(null);

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
  const childPath = (name: string) => path ? `${path}/${name}` : name;
  const upload = async (file?: File) => {
    if (!file) return;
    await run(async () => {
      const resp = await fetch(`/api/sites/${site.id}/files/upload?path=${encodeURIComponent(childPath(file.name))}`, {
        method: "POST", body: file, credentials: "same-origin",
      });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw new ApiError(resp.status, body.error || `upload failed (${resp.status})`);
      }
    }, `${file.name} uploaded`);
    if (uploadInput.current) uploadInput.current.value = "";
    load(path);
  };
  const createFolder = () => {
    const name = prompt("Folder name")?.trim();
    if (!name) return;
    run(() => api.post(`/api/sites/${site.id}/files/manage`, { operation: "mkdir", path: childPath(name) }), "Folder created").then((ok) => ok && load(path));
  };
  const rename = (e: FileEntry) => {
    const name = prompt("New name", e.name)?.trim();
    if (!name || name === e.name) return;
    run(() => api.post(`/api/sites/${site.id}/files/manage`, { operation: "rename", path: childPath(e.name), dest: childPath(name) }), "Renamed").then((ok) => ok && load(path));
  };
  const remove = (e: FileEntry) => {
    if (!confirm(`Delete ${e.name}${e.is_dir ? " and everything inside it" : ""}? This cannot be undone.`)) return;
    run(() => api.post(`/api/sites/${site.id}/files/manage`, { operation: "delete", path: childPath(e.name) }), "Deleted").then((ok) => ok && load(path));
  };

  return (
    <>
      <div className="crumb">
        <a onClick={() => load("")} className="crumb-link">{site.domain}</a>
        {path && " / " + path}
      </div>
      <div className="row mb">
        <input ref={uploadInput} type="file" hidden onChange={(e) => upload(e.target.files?.[0])} />
        <button className="small" disabled={busy} onClick={() => uploadInput.current?.click()}><Icon.upload /> Upload file</button>
        <button className="ghost small" disabled={busy} onClick={createFolder}><Icon.plus /> New folder</button>
        <span className="note tiny">Up to 16 MB here; use SFTP for bulk transfers.</span>
      </div>
      <div className="table-wrap">
        {path && <div className="filerow" onClick={up}><span className="ico"><Icon.arrowUp /></span> <span className="dim">..</span></div>}
        {entries.map((e) => (
          <div className="filerow" key={e.name} onClick={() => open(e)}>
            <span className="ico">{e.is_dir ? <Icon.folder /> : <Icon.file />}</span>
            <span style={{ flex: 1 }}>{e.name}</span>
            <span className="dim3 mono" style={{ fontSize: 12 }}>{e.mode}</span>
            <span className="dim3" style={{ fontSize: 12, width: 80, textAlign: "right" }}>{e.is_dir ? "" : fmtBytes(e.size)}</span>
            {!e.is_dir && <button className="icon-btn" title="Download" onClick={(ev) => { ev.stopPropagation(); window.location.href = `/api/sites/${site.id}/files/download?path=${encodeURIComponent(childPath(e.name))}`; }}><Icon.download /></button>}
            <button className="icon-btn" title="Rename" onClick={(ev) => { ev.stopPropagation(); rename(e); }}><Icon.code /></button>
            <button className="icon-btn danger" title="Delete" onClick={(ev) => { ev.stopPropagation(); remove(e); }}><Icon.trash /></button>
          </div>
        ))}
        {entries.length === 0 && <div className="filerow dim">Empty directory</div>}
      </div>
      {toast.node}

      {editing && (
        <Modal title={editing.path} onClose={() => setEditing(null)} wide>
          <textarea value={editing.content} onChange={(e) => setEditing({ ...editing, content: e.target.value })} rows={20} />
          <div className="row end mt">
            <button className="ghost" onClick={() => setEditing(null)}>Cancel</button>
            <button onClick={save} disabled={busy}>{busy ? "Saving…" : "Save"}</button>
          </div>
        </Modal>
      )}
    </>
  );
}
