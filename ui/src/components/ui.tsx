import { ReactNode, useEffect, useRef, useState } from "react";
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

// Modal shell — closes on Escape and backdrop click, restores focus to the
// element that opened it, and is announced as a dialog to assistive tech.
export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKey);
    ref.current?.focus();
    return () => { document.removeEventListener("keydown", onKey); opener?.focus?.(); };
  }, [onClose]);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div ref={ref} tabIndex={-1} role="dialog" aria-modal="true" aria-label={title}
        className={"card modal" + (wide ? " wide" : "")} onClick={(e) => e.stopPropagation()}>
        <div className="row between mb">
          <h2 style={{ margin: 0 }}>{title}</h2>
          <button className="ghost tiny" aria-label="Close" onClick={onClose}>✕</button>
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

// Reusable polling hook. Exposes an error so pages can distinguish "still
// loading" from "loaded, empty" from "failed" instead of spinning forever.
export function usePoll<T>(path: string, intervalMs = 8000): { data: T | null; error: string | null; reload: () => void } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  const load = () =>
    api.get<T>(path)
      .then((d) => { if (alive.current) { setData(d); setError(null); } })
      .catch((e) => { if (alive.current) setError(e instanceof Error ? e.message : "Failed to load"); });
  useEffect(() => {
    alive.current = true;
    load();
    const t = intervalMs > 0 ? setInterval(load, intervalMs) : undefined;
    return () => { alive.current = false; if (t) clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);
  return { data, error, reload: load };
}

// LoadState renders a consistent spinner/error for a usePoll result. Returns
// null when data is present so the caller renders normally.
export function LoadState({ data, error, onRetry }: { data: unknown; error: string | null; onRetry?: () => void }) {
  if (error) {
    return (
      <div className="error-box">
        <span>{error}</span>
        {onRetry && <button className="ghost tiny" style={{ marginLeft: "auto" }} onClick={onRetry}>Retry</button>}
      </div>
    );
  }
  if (data === null) return <div className="empty"><span className="spinner" /></div>;
  return null;
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
