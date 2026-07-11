import { useEffect, useState } from "react";
import { api, fmtAgo } from "../api";
import { AuditEvent } from "../types";

export default function Audit() {
  const [events, setEvents] = useState<AuditEvent[]>([]);

  useEffect(() => {
    api.get<AuditEvent[]>("/api/audit").then((e) => setEvents(e ?? [])).catch(() => undefined);
  }, []);

  return (
    <>
      <h1>Audit log</h1>
      <p className="sub">Every administrative action, immutably recorded.</p>
      {events.length === 0 ? (
        <p className="dim">No events yet.</p>
      ) : (
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
                <td className="mono">{e.action}</td>
                <td>{e.subject}</td>
                <td className="dim">{e.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
