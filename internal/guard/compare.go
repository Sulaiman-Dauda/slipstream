package guard

import (
	"fmt"
	"time"
)

// Noise floors: percentage regressions only count when the absolute delta
// is big enough to matter. Without these, a small site "regresses" 25% by
// allocating two more megabytes.
const (
	minP95DeltaMillis = 25.0
	minQueryDelta     = 5.0
	minMemDeltaBytes  = 32 << 20
)

// Compare evaluates a candidate against the production baseline and renders
// the promote/block verdict.
//
// Rules:
//   - any regression beyond a threshold (and its noise floor) → block
//   - regression beyond half a threshold → warn
//   - new server errors on the candidate → block, always
func Compare(baseline, candidate []Sample, t Thresholds) Report {
	r := Report{
		Verdict:    VerdictPass,
		Baseline:   baseline,
		Candidate:  candidate,
		Thresholds: t,
		MeasuredAt: time.Now().UTC(),
	}

	base := indexByPath(baseline)
	for _, c := range candidate {
		b, ok := base[c.Path]
		if !ok {
			continue
		}

		// Errors: absolute, not relative — one new 500 is disqualifying.
		candErrPct := pct(c.Errors, c.Requests)
		baseErrPct := pct(b.Errors, b.Requests)
		if t.MaxErrorRatePct > 0 && candErrPct > baseErrPct && candErrPct > t.MaxErrorRatePct {
			r.block("%s: error rate %.1f%% (baseline %.1f%%)", c.Path, candErrPct, baseErrPct)
			continue
		}

		if t.MaxP95IncreasePct > 0 && c.P95Millis-b.P95Millis >= minP95DeltaMillis {
			r.judge(increasePct(b.P95Millis, c.P95Millis), t.MaxP95IncreasePct,
				"%s: p95 %.0fms → %.0fms (+%.0f%%)", c.Path, b.P95Millis, c.P95Millis)
		}
		if t.MaxQueryIncreasePct > 0 && b.AvgQueries > 0 && c.AvgQueries-b.AvgQueries >= minQueryDelta {
			r.judge(increasePct(b.AvgQueries, c.AvgQueries), t.MaxQueryIncreasePct,
				"%s: queries %.0f → %.0f (+%.0f%%)", c.Path, b.AvgQueries, c.AvgQueries)
		}
		if t.MaxMemIncreasePct > 0 && b.PeakMemBytes > 0 && c.PeakMemBytes-b.PeakMemBytes >= minMemDeltaBytes {
			r.judge(increasePct(float64(b.PeakMemBytes), float64(c.PeakMemBytes)), t.MaxMemIncreasePct,
				"%s: peak memory %dMB → %dMB (+%.0f%%)", c.Path, b.PeakMemBytes>>20, c.PeakMemBytes>>20)
		}
	}
	return r
}

// BaselineDrift compares two measurements of the same production target taken
// either side of the candidate run.
//
// Guard's promise is that a promotion cannot silently make a site slower. That
// only holds if the two measurements differ by the candidate and nothing else.
// On a contended box they do not: a staging clone competing for one CPU and a
// fixed pool of memory slows production too, and the candidate then carries the
// blame for the machine. Measuring production twice makes that visible. Any
// movement past the same threshold used to call a regression means the ground
// shifted, and the run tells you so instead of blaming the change.
func BaselineDrift(first, second []Sample, t Thresholds) []string {
	if t.MaxP95IncreasePct <= 0 {
		return nil
	}
	var reasons []string
	idx := indexByPath(second)
	for _, a := range first {
		b, ok := idx[a.Path]
		if !ok {
			continue
		}
		lo, hi := a.P95Millis, b.P95Millis
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo <= 0 || hi-lo < minP95DeltaMillis {
			continue
		}
		if inc := increasePct(lo, hi); inc > t.MaxP95IncreasePct {
			reasons = append(reasons, fmt.Sprintf(
				"%s: production itself moved %.0fms → %.0fms (%.0f%%) while the candidate was measured",
				a.Path, a.P95Millis, b.P95Millis, inc))
		}
	}
	return reasons
}

// Inconclusive builds the report for a run whose baselines disagreed.
func Inconclusive(baseline, candidate []Sample, t Thresholds, reasons []string) Report {
	return Report{
		Verdict:    VerdictInconclusive,
		Reasons:    reasons,
		Baseline:   baseline,
		Candidate:  candidate,
		Thresholds: t,
		MeasuredAt: time.Now().UTC(),
	}
}

// judge escalates the verdict when an increase crosses warn (half) or block
// (full) thresholds. The format receives the extra args plus the increase.
func (r *Report) judge(incPct, threshold float64, format string, args ...any) {
	if incPct <= 0 {
		return
	}
	args = append(args, incPct)
	switch {
	case incPct > threshold:
		r.block(format, args...)
	case incPct > threshold/2:
		r.warn(format, args...)
	}
}

func (r *Report) block(format string, args ...any) {
	r.Verdict = VerdictBlock
	r.Reasons = append(r.Reasons, "BLOCK: "+fmt.Sprintf(format, args...))
}

func (r *Report) warn(format string, args ...any) {
	if r.Verdict == VerdictPass {
		r.Verdict = VerdictWarn
	}
	r.Reasons = append(r.Reasons, "WARN: "+fmt.Sprintf(format, args...))
}

func indexByPath(samples []Sample) map[string]Sample {
	m := make(map[string]Sample, len(samples))
	for _, s := range samples {
		m[s.Path] = s
	}
	return m
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

func increasePct(base, cand float64) float64 {
	if base <= 0 {
		return 0
	}
	return (cand - base) * 100 / base
}

// DefaultProbePaths are measured when a site has no recorded traffic
// sample yet.
var DefaultProbePaths = []string{"/", "/?slipstream-probe=uncached"}
