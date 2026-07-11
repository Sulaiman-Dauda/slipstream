import { GuardReport, GuardSample } from "../types";
import { Icon } from "../icons";

// Visualizes a Performance Guard comparison: baseline (production) vs
// candidate (staging), metric by metric, with the delta highlighted.

function pct(base: number, cand: number): number {
  if (base <= 0) return 0;
  return Math.round(((cand - base) / base) * 100);
}

function DeltaBar({ label, base, cand, unit, betterLower = true }: { label: string; base: number; cand: number; unit: string; betterLower?: boolean }) {
  const change = pct(base, cand);
  const worse = betterLower ? change > 8 : change < -8;
  const better = betterLower ? change < -8 : change > 8;
  const cls = worse ? "bad" : better ? "good" : "dim";
  const max = Math.max(base, cand, 1);
  return (
    <div style={{ marginBottom: 14 }}>
      <div className="row between" style={{ marginBottom: 5 }}>
        <span className="dim" style={{ fontSize: 13 }}>{label}</span>
        <span className={`badge ${cls} plain`} style={{ fontSize: 12 }}>
          {change > 0 ? "+" : ""}{change}%
        </span>
      </div>
      <div style={{ display: "flex", gap: 6, alignItems: "center", fontSize: 12 }}>
        <span className="dim3" style={{ width: 66 }}>prod</span>
        <div className="meter" style={{ flex: 1, marginTop: 0 }}><div style={{ width: `${(base / max) * 100}%`, background: "var(--text-3)" }} /></div>
        <span className="mono" style={{ width: 70, textAlign: "right" }}>{base.toFixed(unit === "ms" ? 0 : 1)}{unit}</span>
      </div>
      <div style={{ display: "flex", gap: 6, alignItems: "center", fontSize: 12, marginTop: 3 }}>
        <span className="dim3" style={{ width: 66 }}>staging</span>
        <div className={`meter ${cls === "bad" ? "bad" : cls === "good" ? "" : ""}`} style={{ flex: 1, marginTop: 0 }}>
          <div style={{ width: `${(cand / max) * 100}%`, background: worse ? "var(--bad)" : better ? "var(--good)" : "var(--accent)" }} />
        </div>
        <span className="mono" style={{ width: 70, textAlign: "right" }}>{cand.toFixed(unit === "ms" ? 0 : 1)}{unit}</span>
      </div>
    </div>
  );
}

export default function GuardReportView({ report }: { report: GuardReport }) {
  const verdictCls = report.verdict === "pass" ? "good" : report.verdict === "warn" ? "warn" : "bad";
  const base = report.baseline || [];
  const cand = report.candidate || [];
  const pathOf = (arr: GuardSample[], p: string) => arr.find((s) => s.path === p);
  const paths = base.map((s) => s.path);

  return (
    <div className="card">
      <div className="row between mb">
        <div className="card-head" style={{ marginBottom: 0 }}><span className={`card-ico ${verdictCls}`}><Icon.gauge /></span><h3 style={{ margin: 0 }}>Performance Guard</h3></div>
        <span className={`badge ${verdictCls}`}>{report.verdict.toUpperCase()}</span>
      </div>

      {report.verdict === "pass" && <div className="ok-box" style={{ marginTop: 0, marginBottom: 14 }}>No regressions — safe to promote.</div>}
      {report.reasons && report.reasons.length > 0 && (
        <div style={{ marginBottom: 14 }}>
          {report.reasons.map((r, i) => (
            <div key={i} className={r.startsWith("BLOCK") ? "error-box" : "info-box"} style={{ marginTop: i ? 6 : 0 }}>{r}</div>
          ))}
        </div>
      )}

      {paths.map((p) => {
        const b = pathOf(base, p);
        const c = pathOf(cand, p);
        if (!b || !c) return null;
        return (
          <div key={p} style={{ marginBottom: 18 }}>
            <div className="mono dim" style={{ fontSize: 12, marginBottom: 8 }}>{p}</div>
            <DeltaBar label="p95 response time" base={b.p95_ms} cand={c.p95_ms} unit="ms" />
            {(b.avg_queries > 0 || c.avg_queries > 0) && <DeltaBar label="DB queries / request" base={b.avg_queries} cand={c.avg_queries} unit="" />}
            {(b.peak_mem_bytes > 0 || c.peak_mem_bytes > 0) && <DeltaBar label="peak memory" base={b.peak_mem_bytes / 1048576} cand={c.peak_mem_bytes / 1048576} unit="MB" />}
          </div>
        );
      })}
    </div>
  );
}
