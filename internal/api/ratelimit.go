package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a simple in-memory fixed-window counter. Panel login
// volume is tiny, so in-memory (reset on restart) is the right trade-off
// against a persistent store.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
	lastGC time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, max: max, window: window}
}

// allow records an attempt and reports whether it is within the limit.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-r.window)

	if now.Sub(r.lastGC) > r.window {
		for k, times := range r.hits {
			if len(times) == 0 || times[len(times)-1].Before(cutoff) {
				delete(r.hits, k)
			}
		}
		r.lastGC = now
	}

	kept := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.max {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}

// reset clears the counter for a key (called after a successful login).
func (r *rateLimiter) reset(key string) {
	r.mu.Lock()
	delete(r.hits, key)
	r.mu.Unlock()
}

// clientIP returns the address of whoever actually made the request.
//
// panel-api listens on loopback and is always fronted by the panel's own
// nginx, so r.RemoteAddr is 127.0.0.1 for every request. Using it alone makes
// the audit log record every login as "127.0.0.1" — useless for answering
// "where did this admin session come from?" — and collapses login rate
// limiting onto a single bucket shared by every source.
//
// So: when (and only when) the connection comes from loopback, believe the
// X-Real-IP header our own nginx sets. nginx assigns it from $remote_addr,
// overwriting anything the client sent, so it cannot be spoofed through the
// proxy. A request arriving from anywhere else is not from our proxy and its
// headers are ignored entirely.
//
// This is deliberately the opposite of CloudPanel's default, which trusts
// forwarded headers from every source (set_real_ip_from 0.0.0.0/0) and can
// therefore be told any source address by the client.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopback(host) {
		return host
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		if ip := net.ParseIP(real); ip != nil {
			return ip.String()
		}
	}
	return host
}

// isLoopback reports whether an address is the local machine.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
