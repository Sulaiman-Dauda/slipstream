package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// panel-api sits behind the panel's own nginx on loopback, so RemoteAddr is
// always 127.0.0.1. Without trusting the proxy header the audit log records
// every login as 127.0.0.1 and rate limiting shares one bucket for the world.
// But the header must ONLY be believed when it came through that proxy —
// trusting it from any source is how CloudPanel's IP blocking is bypassed.
func TestClientIPTrustsProxyOnlyFromLoopback(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		realIP     string
		want       string
	}{
		{"loopback with proxy header", "127.0.0.1:5252", "203.0.113.9", "203.0.113.9"},
		{"loopback without header", "127.0.0.1:5252", "", "127.0.0.1"},
		{"loopback with junk header", "127.0.0.1:5252", "not-an-ip", "127.0.0.1"},
		{"ipv6 loopback with header", "[::1]:5252", "203.0.113.9", "203.0.113.9"},
		// The important one: a direct (non-proxied) request must not be able
		// to claim any address it likes.
		{"direct request spoofing header", "198.51.100.7:44321", "203.0.113.9", "198.51.100.7"},
		{"direct request", "198.51.100.7:44321", "", "198.51.100.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
			r.RemoteAddr = c.remoteAddr
			if c.realIP != "" {
				r.Header.Set("X-Real-IP", c.realIP)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP() = %q, want %q", got, c.want)
			}
		})
	}
}
