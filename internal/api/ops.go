package api

import (
	"net/http"
	"strconv"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// --- Services ---

func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	var res rpc.ServiceStatusResult
	if err := s.Agent.Call(rpc.MethodServiceStatus, nil, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res.Services)
}

var restartableServices = map[string]bool{"nginx": true, "php-fpm": true, "mariadb": true, "redis": true}

func (s *Server) handleRestartService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !restartableServices[name] {
		respondErr(w, http.StatusBadRequest, "unknown service")
		return
	}
	var out map[string]string
	if err := s.Agent.Call(rpc.MethodRestartService, rpc.ServiceParams{Name: name}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "service.restart", name, "")
	respond(w, http.StatusOK, out)
}

// --- Logs ---

func (s *Server) handleReadLog(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	site := r.URL.Query().Get("site")
	if user, _ := s.sessionUser(r); user.Role == "operator" {
		managed, err := s.Store.GetSiteByDomain(site)
		if err != nil || !s.canAccessSite(r, managed.ID) {
			respondErr(w, http.StatusNotFound, "log not found")
			return
		}
	}
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 200
	}
	var res rpc.TailLogResult
	if err := s.Agent.Call(rpc.MethodTailLog, rpc.TailLogParams{Source: source, Site: site, Lines: lines}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

// --- Firewall ---

func (s *Server) handleFirewallStatus(w http.ResponseWriter, r *http.Request) {
	var res rpc.FirewallStatusResult
	if err := s.Agent.Call(rpc.MethodFirewallStatus, nil, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) handleFirewallRule(w http.ResponseWriter, r *http.Request) {
	var req rpc.FirewallRuleParams
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	var out map[string]string
	if err := s.Agent.Call(rpc.MethodFirewallRule, req, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "firewall.rule", req.Action, "")
	respond(w, http.StatusOK, out)
}

// --- Panel certificate ---

type panelCertRequest struct {
	Domain string `json:"domain"`
	Email  string `json:"email"`
}

func (s *Server) handlePanelCertificate(w http.ResponseWriter, r *http.Request) {
	var req panelCertRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Email == "" {
		req.Email, _ = s.Store.GetSetting("acme_email", "")
	}
	if req.Domain == "" || req.Email == "" {
		respondErr(w, http.StatusBadRequest, "panel domain and acme email are required")
		return
	}
	port := s.PanelPort
	if port == 0 {
		port = 5252
	}
	// Persist so the scheduler can auto-renew and the UI can show it.
	s.Store.SetSetting("panel_domain", req.Domain)
	s.Store.SetSetting("acme_email", req.Email)

	task, err := s.runTask("panel.certificate", 0, func(progress func(int, string)) error {
		progress(30, "Requesting certificate for "+req.Domain)
		var out map[string]string
		if err := s.Agent.Call(rpc.MethodPanelCert, rpc.PanelCertParams{Domain: req.Domain, Email: req.Email, Port: port}, &out); err != nil {
			return err
		}
		progress(100, "Panel certificate installed — the panel will restart")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "panel.certificate", req.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// --- Self-update ---

func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		Version string `json:"version"`
	}
	decode(r, &req)
	if req.BaseURL == "" {
		req.BaseURL, _ = s.Store.GetSetting("update_url", "")
	}
	if req.BaseURL == "" {
		respondErr(w, http.StatusBadRequest, "no update URL configured")
		return
	}
	s.Store.Audit(s.actor(r), "panel.update", req.Version, "")
	var out rpc.SelfUpdateResult
	if err := s.Agent.Call(rpc.MethodSelfUpdate, rpc.SelfUpdateParams{BaseURL: req.BaseURL, Version: req.Version}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, out)
}
