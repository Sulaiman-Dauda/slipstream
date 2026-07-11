import { useEffect, useState } from "react";
import { api } from "../api";
import { Task } from "../types";

const statusBadge: Record<Task["status"], string> = {
  pending: "dim",
  running: "accent",
  succeeded: "good",
  failed: "bad",
};

// TaskFeed subscribes to the panel's SSE stream and shows live progress.
export default function TaskFeed({ siteId, limit = 8 }: { siteId?: number; limit?: number }) {
  const [tasks, setTasks] = useState<Task[]>([]);

  useEffect(() => {
    let source: EventSource | null = new EventSource("/api/events");
    source.addEventListener("tasks", (ev) => {
      try {
        setTasks(JSON.parse((ev as MessageEvent).data) ?? []);
      } catch {
        /* skip malformed frame */
      }
    });
    source.onerror = () => {
      // Fall back to one-shot fetch; the browser retries SSE on its own.
      api.get<Task[]>("/api/tasks").then((t) => setTasks(t ?? [])).catch(() => undefined);
    };
    return () => {
      source?.close();
      source = null;
    };
  }, []);

  const visible = tasks.filter((t) => !siteId || t.site_id === siteId).slice(0, limit);
  if (visible.length === 0) return <p className="dim">No recent activity.</p>;

  return (
    <div className="task-feed">
      {visible.map((t) => (
        <div className="task-row" key={t.id}>
          <span className="kind">{t.kind}</span>
          <span className="msg">{t.error || t.message || "queued"}</span>
          {t.status === "running" && (
            <div className="progress">
              <div style={{ width: `${t.progress}%` }} />
            </div>
          )}
          <span className={`badge ${statusBadge[t.status]}`}>{t.status}</span>
        </div>
      ))}
    </div>
  );
}
