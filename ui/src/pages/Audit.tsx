import { useEffect, useState } from "react";
import { api, fmtAgo } from "../api";
import { AuditEvent } from "../types";
import { Skeleton } from "../components/ui";
import { Icon } from "../icons";

function actionTone(action: string): string {
  if (action.includes("delete") || action.includes("disable") || action.includes("block")) return "bad";
  if (action.includes("create") || action.includes("enable") || action.includes("login")) return "good";
  return "dim";
}

export default function Audit() {
  const [events, setEvents] = useState<AuditEvent[] | null>(null);

  useEffect(() => {
    api.get<AuditEvent[]>("/api/audit").then((e) => setEvents(e ?? [])).catch(() => setEvents([]));
  }, []);

  return (
    <>
      <h1>Audit log</h1>
      <p className="sub">Every administrative action, immutably recorded.</p>
      {events === null ? (
        <Skeleton count={5} />
      ) : events.length === 0 ? (
        <div className="card empty">
          <div className="big"><Icon.audit /></div>
          <div className="title">No events yet</div>
          <p>Administrative actions will appear here as they happen.</p>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Subject</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td className="dim">{fmtAgo(e.created_at)}</td>
                  <td>{e.actor}</td>
                  <td><span className={`badge ${actionTone(e.action)} plain mono`}>{e.action}</span></td>
                  <td>{e.subject}</td>
                  <td className="dim">{e.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
