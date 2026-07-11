package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple in-memory fixed-window counter. Panel login
// volume is tiny, so in-memory (reset on restart) is the right trade-off
// against a persistent store.
type rateLimiter struct {
	mu       sync.Mutex
	hits     map[string][]time.Time
	max      int
	window   time.Duration
	lastGC   time.Time
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

// clientIP extracts the best-effort client address from a request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
