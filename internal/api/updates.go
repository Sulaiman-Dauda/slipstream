package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/slipstream-panel/slipstream/internal/version"
)

// The panel tells an operator when a newer release exists and links to what
// changed. It never installs anything on its own: the update is a button, and
// pressing it goes through the same health-gated, provenance-checked path as
// `POST /api/panel/update`.
//
// The check runs only when someone opens the panel and asks, never on a timer,
// and it fetches a public URL without sending anything about this server. That
// keeps the "no phone-home" promise on the front page honest, and the FAQ says
// plainly what the request is. Setting update_check to 0 turns it off entirely.
const (
	releasesLatest = "https://github.com/Sulaiman-Dauda/slipstream/releases/latest"
	changelogURL   = "https://slipstreampanel.com/changelog"
	checkTTL       = 6 * time.Hour
)

type updateCache struct {
	mu        sync.Mutex
	latest    string
	checkedAt time.Time
	lastErr   string
}

type updateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"update_available"`
	NotesURL  string `json:"notes_url,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	Enabled   bool   `json:"check_enabled"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	out := updateStatus{Current: version.Version, Enabled: true}

	if v, _ := s.Store.GetSetting("update_check", ""); v == "0" || v == "false" {
		out.Enabled = false
		respond(w, http.StatusOK, out)
		return
	}

	latest, checkedAt, err := s.latestRelease(r.URL.Query().Get("refresh") == "1")
	if err != "" {
		out.Error = err
	}
	out.Latest = latest
	if !checkedAt.IsZero() {
		out.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
	}
	if latest != "" {
		out.Available = newerThan(latest, version.Version)
		out.NotesURL = changelogURL + "#" + anchorFor(latest)
	}
	respond(w, http.StatusOK, out)
}

// latestRelease resolves the newest published tag, cached so that opening the
// dashboard repeatedly does not mean a request each time.
func (s *Server) latestRelease(force bool) (string, time.Time, string) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()

	if !force && time.Since(s.updates.checkedAt) < checkTTL && s.updates.latest != "" {
		return s.updates.latest, s.updates.checkedAt, s.updates.lastErr
	}

	// /releases/latest redirects to /releases/tag/<version>, so the tag comes
	// out of the Location header. No API token, and no rate limit worth worrying
	// about for a check this rare.
	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(releasesLatest)
	if err != nil {
		s.updates.lastErr = "could not reach the release feed"
		s.updates.checkedAt = time.Now()
		return s.updates.latest, s.updates.checkedAt, s.updates.lastErr
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		s.updates.lastErr = "no published release found"
		s.updates.checkedAt = time.Now()
		return s.updates.latest, s.updates.checkedAt, s.updates.lastErr
	}

	s.updates.latest, s.updates.checkedAt, s.updates.lastErr = tag, time.Now(), ""
	return s.updates.latest, s.updates.checkedAt, ""
}

// newerThan compares vX.Y.Z tags numerically, so v0.2.10 sorts above v0.2.9
// rather than below it the way a string comparison would.
func newerThan(candidate, current string) bool {
	c, cur := parseVersion(candidate), parseVersion(current)
	if c == nil || cur == nil {
		return false // a dev build, or something unparseable: never nag
	}
	for i := range c {
		if c[i] != cur[i] {
			return c[i] > cur[i]
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" || v == "dev" {
		return nil
	}
	parts := strings.SplitN(v, "-", 2) // ignore any -14-gabc123 suffix
	fields := strings.Split(parts[0], ".")
	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i >= len(fields) {
			continue
		}
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// anchorFor turns v0.2.2 into the heading anchor the changelog page renders.
func anchorFor(tag string) string {
	return fmt.Sprintf("v%s", strings.ReplaceAll(strings.TrimPrefix(tag, "v"), ".", ""))
}
