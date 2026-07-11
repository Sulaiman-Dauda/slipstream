import { ReactNode, useEffect, useState } from "react";
import { api } from "../api";

// Toast — a lightweight global notice, driven by a window event.
export function useToast() {
  const [msg, setMsg] = useState<{ text: string; kind: "ok" | "error" } | null>(null);
  useEffect(() => {
    if (!msg) return;
    const t = setTimeout(() => setMsg(null), 4000);
    return () => clearTimeout(t);
  }, [msg]);
  return {
    node: msg ? <div className={msg.kind === "ok" ? "ok-box" : "error-box"}>{msg.text}</div> : null,
    ok: (text: string) => setMsg({ text, kind: "ok" }),
    err: (text: string) => setMsg({ text, kind: "error" }),
  };
}

// Modal shell.
export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className={"card modal" + (wide ? " wide" : "")} onClick={(e) => e.stopPropagation()}>
        <div className="row between mb">
          <h2 style={{ margin: 0 }}>{title}</h2>
          <button className="ghost tiny" onClick={onClose}>✕</button>
        </div>
        {children}
      </div>
    </div>
  );
}

// useAsyncAction wraps an API call with busy state and toast feedback.
export function useAction(toast: ReturnType<typeof useToast>) {
  const [busy, setBusy] = useState(false);
  return {
    busy,
    run: async (fn: () => Promise<unknown>, okMsg?: string) => {
      setBusy(true);
      try {
        await fn();
        if (okMsg) toast.ok(okMsg);
        return true;
      } catch (e) {
        toast.err(e instanceof Error ? e.message : "Something went wrong");
        return false;
      } finally {
        setBusy(false);
      }
    },
  };
}

// Reusable polling hook.
export function usePoll<T>(path: string, intervalMs = 8000): [T | null, () => void] {
  const [data, setData] = useState<T | null>(null);
  const load = () => api.get<T>(path).then(setData).catch(() => undefined);
  useEffect(() => {
    load();
    if (intervalMs <= 0) return;
    const t = setInterval(load, intervalMs);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);
  return [data, load];
}

export function StatusBadge({ status }: { status: string }) {
  const map: Record<string, string> = {
    active: "good", running: "accent", provisioning: "accent", succeeded: "good",
    passed: "good", pass: "good", promoted: "good", enabled: "good",
    error: "bad", failed: "bad", blocked: "bad", down: "bad",
    warn: "warn", pending: "dim", deleting: "warn", rolled_back: "warn", created: "dim",
  };
  return <span className={`badge ${map[status] || "dim"}`}>{status.replace(/_/g, " ")}</span>;
}
