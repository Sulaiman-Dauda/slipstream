// Package guard implements Performance Guard: before a staging release is
// promoted to production, replay representative requests against both,
// compare latency, errors, database load and memory, and block promotions
// that regress. The product promise: deployments cannot silently make your
// site slower.
package guard

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Thresholds define what counts as a regression. Zero values disable a check.
type Thresholds struct {
	MaxP95IncreasePct   float64 `json:"max_p95_increase_pct"`
	MaxQueryIncreasePct float64 `json:"max_query_increase_pct"`
	MaxMemIncreasePct   float64 `json:"max_mem_increase_pct"`
	MaxErrorRatePct     float64 `json:"max_error_rate_pct"`
}

// DefaultThresholds are the shipped policy: generous enough to avoid noise,
// tight enough to catch a plugin update that doubles query count.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxP95IncreasePct:   25,
		MaxQueryIncreasePct: 50,
		MaxMemIncreasePct:   30,
		MaxErrorRatePct:     1,
	}
}

// Sample is the measured behaviour of one URL on one target.
type Sample struct {
	Path         string  `json:"path"`
	Requests     int     `json:"requests"`
	Errors       int     `json:"errors"`
	P50Millis    float64 `json:"p50_ms"`
	P95Millis    float64 `json:"p95_ms"`
	AvgQueries   float64 `json:"avg_queries"`
	PeakMemBytes int64   `json:"peak_mem_bytes"`
}

// Verdict is the gate decision.
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictWarn  Verdict = "warn"
	VerdictBlock Verdict = "block"
)

// Report is the full comparison, stored on the deployment record and shown
// in the UI before promotion.
type Report struct {
	Verdict    Verdict    `json:"verdict"`
	Reasons    []string   `json:"reasons,omitempty"`
	Baseline   []Sample   `json:"baseline"`
	Candidate  []Sample   `json:"candidate"`
	Thresholds Thresholds `json:"thresholds"`
	MeasuredAt time.Time  `json:"measured_at"`
}

// Prober replays requests against a target and measures behaviour.
type Prober struct {
	// Target is where requests are sent (usually https://127.0.0.1).
	Target string
	// Host is the virtual host to measure.
	Host string
	// RequestsPerPath is how many samples to take per URL (default 20).
	RequestsPerPath int
	// Concurrency bounds parallel requests (default 4).
	Concurrency int
	// Client, if nil, uses a TLS-insecure localhost client (the panel talks
	// to its own Nginx over loopback; the cert may be the fallback).
	Client *http.Client
	// WarmupRequests are sent per path and discarded (default 3) so cold
	// caches don't poison the comparison.
	WarmupRequests int
}

var metricsRe = regexp.MustCompile(`slipstream queries=(\d+) time=([0-9.]+) mem=(\d+)`)

func (p *Prober) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// Measure samples every path and returns per-path behaviour.
func (p *Prober) Measure(ctx context.Context, paths []string) ([]Sample, error) {
	if p.RequestsPerPath <= 0 {
		p.RequestsPerPath = 20
	}
	if p.Concurrency <= 0 {
		p.Concurrency = 4
	}
	if p.WarmupRequests < 0 {
		p.WarmupRequests = 0
	} else if p.WarmupRequests == 0 {
		p.WarmupRequests = 3
	}

	samples := make([]Sample, len(paths))
	for i, path := range paths {
		s, err := p.measurePath(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("measure %s: %w", path, err)
		}
		samples[i] = s
	}
	return samples, nil
}

func (p *Prober) measurePath(ctx context.Context, path string) (Sample, error) {
	client := p.client()

	for i := 0; i < p.WarmupRequests; i++ {
		p.oneRequest(ctx, client, path)
	}

	type result struct {
		millis  float64
		queries float64
		mem     int64
		failed  bool
	}
	results := make([]result, p.RequestsPerPath)
	sem := make(chan struct{}, p.Concurrency)
	var wg sync.WaitGroup
	for i := 0; i < p.RequestsPerPath; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(n int) {
			defer wg.Done()
			defer func() { <-sem }()
			millis, queries, mem, err := p.oneRequest(ctx, client, path)
			results[n] = result{millis: millis, queries: queries, mem: mem, failed: err != nil}
		}(i)
	}
	wg.Wait()

	s := Sample{Path: path, Requests: p.RequestsPerPath}
	var latencies []float64
	var queriesSum float64
	var queriesN int
	for _, r := range results {
		if r.failed {
			s.Errors++
			continue
		}
		latencies = append(latencies, r.millis)
		if r.queries > 0 {
			queriesSum += r.queries
			queriesN++
		}
		if r.mem > s.PeakMemBytes {
			s.PeakMemBytes = r.mem
		}
	}
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		s.P50Millis = percentile(latencies, 50)
		s.P95Millis = percentile(latencies, 95)
	}
	if queriesN > 0 {
		s.AvgQueries = queriesSum / float64(queriesN)
	}
	return s, nil
}

// oneRequest performs a single GET and extracts connector metrics when the
// page carries them.
func (p *Prober) oneRequest(ctx context.Context, client *http.Client, path string) (millis, queries float64, mem int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Target+path, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	req.Host = p.Host
	// Cache-busting header is deliberately absent: the guard measures the
	// site as visitors experience it, cache included.
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	millis = float64(time.Since(start).Microseconds()) / 1000
	if resp.StatusCode >= 500 {
		return millis, 0, 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	if readErr != nil {
		return millis, 0, 0, readErr
	}
	if m := metricsRe.FindSubmatch(body); m != nil {
		queries, _ = strconv.ParseFloat(string(m[1]), 64)
		mem, _ = strconv.ParseInt(string(m[3]), 10, 64)
	}
	return millis, queries, mem, nil
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * pct / 100)
	return sorted[idx]
}
