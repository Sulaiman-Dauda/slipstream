import { FormEvent, useState } from "react";
import { api, fmtAgo } from "../api";
import { PanelUser } from "../types";
import { Modal, useAction, usePoll, useToast } from "../components/ui";

export default function Users() {
  const { data: users, error, reload } = usePoll<PanelUser[]>("/api/users", 0);
  const [creating, setCreating] = useState(false);
  const toast = useToast();
  const { run } = useAction(toast);

  return (
    <>
      <div className="topbar">
        <div><h1>Users</h1><p className="sub">Panel administrators. Everyone here can manage the whole server.</p></div>
        <button onClick={() => setCreating(true)}>+ Add user</button>
      </div>
      {toast.node}
      {error && <div className="error-box">{error}</div>}
      <div className="table-wrap">
        <table>
          <thead><tr><th>Email</th><th>Role</th><th>2FA</th><th>Added</th><th></th></tr></thead>
          <tbody>
            {(users || []).map((u) => (
              <tr key={u.id}>
                <td>{u.email}</td>
                <td><span className="badge dim plain">{u.role}</span></td>
                <td>{u.totp_enabled ? <span className="badge good">on</span> : <span className="badge dim">off</span>}</td>
                <td className="dim">{fmtAgo(u.created_at)}</td>
                <td><button className="danger tiny" onClick={() => { if (confirm(`Remove ${u.email}?`)) run(() => api.del(`/api/users/${u.id}`), "User removed").then(reload); }}>Remove</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {creating && <CreateUser onClose={() => { setCreating(false); reload(); }} />}
    </>
  );
}

function CreateUser({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("admin");
  const submit = (e: FormEvent) => { e.preventDefault(); run(() => api.post("/api/users", { email, password, role }), "User added").then((ok) => ok && onClose()); };
  return (
    <Modal title="Add user" onClose={onClose}>
      <form onSubmit={submit}>
        <label>Email</label>
        <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
        <label>Password <span className="hint">12+ characters</span></label>
        <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={12} required />
        <label>Role</label>
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          <option value="admin">Admin — full access</option>
          <option value="readonly">Read-only — view but not change</option>
        </select>
        {toast.node}
        <div className="row end mt-lg"><button type="button" className="ghost" onClick={onClose}>Cancel</button><button disabled={busy}>Add user</button></div>
      </form>
    </Modal>
  );
}
