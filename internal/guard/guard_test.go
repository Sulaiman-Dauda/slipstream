package guard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeSite(delay time.Duration, queries int, fail bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		if fail && strings.Contains(r.URL.RawQuery, "uncached") {
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, "<html><body>page</body>\n<!-- slipstream queries=%d time=0.1 mem=%d -->\n</html>", queries, 32<<20)
	}))
}

func measure(t *testing.T, srv *httptest.Server, paths []string) []Sample {
	t.Helper()
	p := &Prober{Target: srv.URL, Host: "example.com", RequestsPerPath: 10, Concurrency: 2, Client: srv.Client()}
	samples, err := p.Measure(context.Background(), paths)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	return samples
}

func TestProberExtractsConnectorMetrics(t *testing.T) {
	srv := fakeSite(0, 42, false)
	defer srv.Close()
	samples := measure(t, srv, []string{"/"})
	if len(samples) != 1 {
		t.Fatal("want one sample")
	}
	s := samples[0]
	if s.AvgQueries != 42 {
		t.Errorf("queries = %f, want 42", s.AvgQueries)
	}
	if s.PeakMemBytes != 32<<20 {
		t.Errorf("mem = %d", s.PeakMemBytes)
	}
	if s.Errors != 0 || s.P95Millis <= 0 {
		t.Errorf("sample: %+v", s)
	}
}

func TestIdenticalDeployPasses(t *testing.T) {
	srv := fakeSite(0, 30, false)
	defer srv.Close()
	base := measure(t, srv, []string{"/"})
	cand := measure(t, srv, []string{"/"})
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictPass {
		t.Fatalf("verdict = %s, reasons = %v", rep.Verdict, rep.Reasons)
	}
}

func TestQueryRegressionBlocks(t *testing.T) {
	base := []Sample{{Path: "/", Requests: 10, P95Millis: 100, AvgQueries: 40}}
	cand := []Sample{{Path: "/", Requests: 10, P95Millis: 110, AvgQueries: 167}}
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictBlock {
		t.Fatalf("verdict = %s, want block; reasons = %v", rep.Verdict, rep.Reasons)
	}
	if len(rep.Reasons) == 0 || !strings.Contains(rep.Reasons[0], "queries 40 → 167") {
		t.Errorf("reasons = %v", rep.Reasons)
	}
}

func TestLatencyWarnZone(t *testing.T) {
	// +15% p95 with a 45ms absolute delta: above half the 25% threshold and
	// above the noise floor → warn, not block.
	base := []Sample{{Path: "/", Requests: 10, P95Millis: 300}}
	cand := []Sample{{Path: "/", Requests: 10, P95Millis: 345}}
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictWarn {
		t.Fatalf("verdict = %s, want warn; reasons = %v", rep.Verdict, rep.Reasons)
	}
}

func TestNoiseFloorsIgnoreTinyDeltas(t *testing.T) {
	// +25% memory but only 2MB absolute, +15ms p95: both below noise floors.
	base := []Sample{{Path: "/", Requests: 10, P95Millis: 60, PeakMemBytes: 8 << 20, AvgQueries: 80}}
	cand := []Sample{{Path: "/", Requests: 10, P95Millis: 75, PeakMemBytes: 10 << 20, AvgQueries: 83}}
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictPass {
		t.Fatalf("noise blocked promotion: %s %v", rep.Verdict, rep.Reasons)
	}
}

func TestNewServerErrorsBlock(t *testing.T) {
	good := fakeSite(0, 30, false)
	defer good.Close()
	bad := fakeSite(0, 30, true)
	defer bad.Close()

	base := measure(t, good, []string{"/?slipstream-probe=uncached"})
	cand := measure(t, bad, []string{"/?slipstream-probe=uncached"})
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictBlock {
		t.Fatalf("verdict = %s, want block; reasons = %v", rep.Verdict, rep.Reasons)
	}
}

func TestFasterCandidateNeverFlagged(t *testing.T) {
	base := []Sample{{Path: "/", Requests: 10, P95Millis: 200, AvgQueries: 80, PeakMemBytes: 64 << 20}}
	cand := []Sample{{Path: "/", Requests: 10, P95Millis: 90, AvgQueries: 30, PeakMemBytes: 32 << 20}}
	rep := Compare(base, cand, DefaultThresholds())
	if rep.Verdict != VerdictPass || len(rep.Reasons) != 0 {
		t.Fatalf("improvement flagged: %s %v", rep.Verdict, rep.Reasons)
	}
}

// Guard's verdict is only meaningful if production behaved the same either
// side of the candidate. On a contended box it does not, and the candidate
// gets blamed for the machine — the failure mode that makes operators start
// passing --force.
func TestBaselineDriftSeparatesTheMachineFromTheChange(t *testing.T) {
	th := DefaultThresholds()

	steady := []Sample{{Path: "/", Requests: 10, P95Millis: 400}}
	same := []Sample{{Path: "/", Requests: 10, P95Millis: 412}}
	if got := BaselineDrift(steady, same, th); len(got) != 0 {
		t.Errorf("a steady box reported drift: %v", got)
	}

	// Under the absolute noise floor, so not drift however large the ratio.
	tiny := []Sample{{Path: "/", Requests: 10, P95Millis: 10}}
	tinyUp := []Sample{{Path: "/", Requests: 10, P95Millis: 30}}
	if got := BaselineDrift(tiny, tinyUp, th); len(got) != 0 {
		t.Errorf("noise floor not applied: %v", got)
	}

	// The real case: production more than doubled while staging was measured.
	moved := []Sample{{Path: "/", Requests: 10, P95Millis: 1100}}
	if got := BaselineDrift(steady, moved, th); len(got) != 1 {
		t.Fatalf("drift not detected, got %v", got)
	}

	// Drift is direction-agnostic: production getting faster mid-run is just
	// as much a moving target as it getting slower.
	if got := BaselineDrift(moved, steady, th); len(got) != 1 {
		t.Errorf("drift missed when production sped up: %v", got)
	}

	rep := Inconclusive(steady, moved, th, BaselineDrift(steady, moved, th))
	if rep.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want inconclusive", rep.Verdict)
	}
	if rep.Verdict == VerdictBlock {
		t.Error("an unstable machine must never be reported as a regression")
	}
}
