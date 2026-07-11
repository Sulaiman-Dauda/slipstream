import { FormEvent, useCallback, useEffect, useState } from "react";
import { api, fmtAgo } from "../../api";
import { CronJob, Site } from "../../types";
import { useAction, useToast } from "../../components/ui";

const presets = [
  { label: "Every 15 minutes", value: "*/15 * * * *" },
  { label: "Hourly", value: "0 * * * *" },
  { label: "Daily at 3am", value: "0 3 * * *" },
  { label: "Weekly (Sun 3am)", value: "0 3 * * 0" },
];

export default function Cron({ site }: { site: Site }) {
  const toast = useToast();
  const { run, busy } = useAction(toast);
  const [jobs, setJobs] = useState<CronJob[]>([]);
  const [schedule, setSchedule] = useState("*/15 * * * *");
  const [command, setCommand] = useState("");
  const [description, setDescription] = useState("");

  const load = useCallback(() => {
    api.get<CronJob[]>(`/api/sites/${site.id}/cron`).then((j) => setJobs(j || [])).catch(() => undefined);
  }, [site.id]);
  useEffect(() => { load(); }, [load]);

  const add = (e: FormEvent) => {
    e.preventDefault();
    run(() => api.post(`/api/sites/${site.id}/cron`, { schedule, command, description }), "Cron job added")
      .then((ok) => { if (ok) { setCommand(""); setDescription(""); load(); } });
  };
  const del = (id: number) => {
    if (!confirm("Remove this scheduled task?")) return;
    run(() => api.del(`/api/cron/${id}`), "Removed").then((ok) => ok && load());
  };

  const wpExample = site.type === "wordpress" || site.type === "woocommerce";

  return (
    <>
      <div className="card mb">
        <h3>New scheduled task</h3>
        <form onSubmit={add}>
          <label>Schedule</label>
          <div className="row">
            <input value={schedule} onChange={(e) => setSchedule(e.target.value)} className="mono" style={{ flex: 1 }} />
            <select style={{ width: 200 }} onChange={(e) => e.target.value && setSchedule(e.target.value)} value="">
              <option value="">Presets…</option>
              {presets.map((p) => <option key={p.value} value={p.value}>{p.label}</option>)}
            </select>
          </div>
          <label>Command</label>
          <input value={command} onChange={(e) => setCommand(e.target.value)} className="mono"
            placeholder={wpExample ? `wp --path=/srv/sites/${site.domain}/current cron event run --due-now` : "/usr/bin/php /srv/.../script.php"} required />
          <label>Description <span className="hint">optional</span></label>
          <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this task does" />
          {toast.node}
          <div className="row end mt"><button disabled={busy}>Add task</button></div>
        </form>
      </div>

      {jobs.length === 0 ? (
        <div className="info-box">No scheduled tasks. WordPress sites benefit from a real cron running <span className="mono">wp cron event run --due-now</span> every few minutes instead of on-request wp-cron.</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Schedule</th><th>Command</th><th>Last run</th><th></th></tr></thead>
            <tbody>
              {jobs.map((j) => (
                <tr key={j.id}>
                  <td className="mono">{j.schedule}</td>
                  <td><div className="mono" style={{ maxWidth: 380, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{j.command}</div>{j.description && <div className="dim3" style={{ fontSize: 12 }}>{j.description}</div>}</td>
                  <td className="dim">{j.last_run ? fmtAgo(j.last_run) : "—"}</td>
                  <td><button className="danger tiny" onClick={() => del(j.id)}>Remove</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
