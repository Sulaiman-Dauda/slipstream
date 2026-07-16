package api

import (
	"net/http"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// handleHealthz is an unauthenticated liveness probe. It confirms the API
// is up and whether the privileged agent is reachable — enough for a load
// balancer or uptime monitor, without leaking sensitive detail.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	agentUp := s.Agent.Call(rpc.MethodPing, nil, nil) == nil
	status := http.StatusOK
	if !agentUp {
		status = http.StatusServiceUnavailable
	}
	respond(w, status, map[string]any{"status": map[bool]string{true: "ok", false: "degraded"}[agentUp], "agent": agentUp})
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	var res rpc.SystemStatusResult
	if err := s.Agent.Call(rpc.MethodSystemStatus, nil, &res); err != nil {
		respondErr(w, http.StatusBadGateway, "agent unavailable: "+err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) handleSystemMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.Store.ListMetrics(288)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, metrics)
}

// handleListDrift runs a live drift check against every managed file,
// records new drift events, and returns the open set.
func (s *Server) handleListDrift(w http.ResponseWriter, r *http.Request) {
	expected, err := s.Store.ManagedFiles()
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var res rpc.DriftResult
	if len(expected) > 0 {
		if err := s.Agent.Call(rpc.MethodCheckDrift, rpc.DriftParams{Expected: expected}, &res); err != nil {
			respondErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	open, err := s.Store.ListDriftEvents(false)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	known := map[string]bool{}
	for _, e := range open {
		known[e.Path+e.ActualHash] = true
	}
	for _, d := range res.Drifted {
		if known[d.Path+d.ActualHash] {
			continue
		}
		if ev, err := s.Store.CreateDriftEvent(state.DriftEvent{
			Path: d.Path, ExpectedHash: d.ExpectedHash, ActualHash: d.ActualHash,
		}); err == nil {
			open = append([]state.DriftEvent{ev}, open...)
		}
	}
	respond(w, http.StatusOK, open)
}

type resolveDriftRequest struct {
	Action string `json:"action"` // restore | accept
}

// handleResolveDrift either re-renders managed configuration from desired
// state (restore) or adopts the manual change as the new expected content
// (accept). Nothing is ever silently overwritten.
func (s *Server) handleResolveDrift(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var req resolveDriftRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	events, err := s.Store.ListDriftEvents(false)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var event *state.DriftEvent
	for i := range events {
		if events[i].ID == id {
			event = &events[i]
			break
		}
	}
	if event == nil {
		respondErr(w, http.StatusNotFound, "drift event not found or already resolved")
		return
	}

	switch req.Action {
	case "accept":
		s.Store.RecordManagedFile(event.Path, event.ActualHash)
		s.Store.ResolveDriftEvent(event.ID, state.DriftAccepted)
		s.Store.Audit(s.actor(r), "drift.accept", event.Path, "")
		respond(w, http.StatusOK, map[string]string{"resolved": "accepted"})
	case "restore":
		// Re-render every site whose files could own this path; rendering
		// is idempotent, so re-rendering the owning site restores the file.
		sites, err := s.Store.ListSites()
		if err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		restored := false
		for _, site := range sites {
			if !siteOwnsPath(site, event.Path) {
				continue
			}
			var res rpc.ApplyResult
			if err := s.Agent.Call(rpc.MethodApplySiteConfig, rpc.SiteRef{Site: site}, &res); err != nil {
				respondErr(w, http.StatusBadGateway, err.Error())
				return
			}
			for _, f := range res.Files {
				s.Store.RecordManagedFile(f.Path, f.SHA256)
			}
			restored = true
			break
		}
		if !restored {
			respondErr(w, http.StatusUnprocessableEntity, "no site owns this file; accept the change or remove the file manually")
			return
		}
		s.Store.ResolveDriftEvent(event.ID, state.DriftRestored)
		s.Store.Audit(s.actor(r), "drift.restore", event.Path, "")
		respond(w, http.StatusOK, map[string]string{"resolved": "restored"})
	default:
		respondErr(w, http.StatusBadRequest, "action must be restore or accept")
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	events, err := s.Store.ListAuditEvents(200)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []state.AuditEvent{}
	}
	respond(w, http.StatusOK, events)
}

// Settings the UI may read/write. Secrets (site tokens, db passwords) are
// deliberately excluded.
var editableSettings = map[string]bool{
	"acme_email":        true,
	"backup_repository": true,
	"backup_password":   true,
	"probe_target":      true,
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for key := range editableSettings {
		v, _ := s.Store.GetSetting(key, "")
		if key == "backup_password" && v != "" {
			v = "••••••••" // presence indicator only
		}
		out[key] = v
	}
	respond(w, http.StatusOK, out)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	for key := range req {
		if !editableSettings[key] {
			respondErr(w, http.StatusBadRequest, "unknown setting "+key)
			return
		}
	}
	// A restic repository is encrypted with whatever password was current at
	// init time; changing backup_password (or backup_repository) here only
	// changes what Slipstream will TRY next time -- it does not re-key the
	// actual repository. Confirmed live: changing backup_password to a new
	// value the operator believed was just "updating" it silently locked the
	// panel out of every existing backup with no warning at the time of the
	// change -- the failure only surfaced later, misleadingly, as "config
	// file already exists" on the next scheduled backup (ensureRepo's `cat
	// config` failed on the wrong password, then `init` correctly refused to
	// clobber a repo that already exists). Validate the EFFECTIVE
	// repository+password pairing against the real repository before
	// accepting either field, the same way handleBackupTest already does.
	_, changingRepo := req["backup_repository"]
	_, changingPassword := req["backup_password"]
	if changingRepo || changingPassword {
		repo := req["backup_repository"]
		if !changingRepo {
			repo, _ = s.Store.GetSetting("backup_repository", "")
		}
		password := req["backup_password"]
		if !changingPassword || strings.Contains(password, "•") {
			password, _ = s.Store.GetSetting("backup_password", "")
		}
		if repo != "" && password != "" {
			var out map[string]any
			if err := s.Agent.Call(rpc.MethodTestBackup, rpc.BackupParams{Repository: repo, Password: password}, &out); err != nil {
				respondErr(w, http.StatusBadRequest, "this repository/password pairing does not work, refusing to save (existing backups would become unreachable): "+err.Error())
				return
			}
		}
	}
	for key, value := range req {
		if key == "backup_password" && strings.Contains(value, "•") {
			continue // masked round-trip, not a change
		}
		if err := s.Store.SetSetting(key, value); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.Store.Audit(s.actor(r), "settings.update", strings.Join(keysOf(req), ","), "")
	s.handleGetSettings(w, r)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
