package api

import (
	"net/http"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

func isWordPress(t state.SiteType) bool {
	return t == state.SiteWordPress || t == state.SiteWooCommerce
}

func (s *Server) handleWPMagicLogin(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !isWordPress(site.Type) {
		respondErr(w, http.StatusBadRequest, "not a WordPress site")
		return
	}
	var res rpc.WPLoginResult
	if err := s.Agent.Call(rpc.MethodWPMagicLogin, rpc.WPParams{Site: site}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "wp.magic_login", site.Domain, "")
	respond(w, http.StatusOK, res)
}

func (s *Server) handleWPPlugins(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !isWordPress(site.Type) {
		respondErr(w, http.StatusBadRequest, "not a WordPress site")
		return
	}
	var res rpc.WPPluginsResult
	if err := s.Agent.Call(rpc.MethodWPPlugins, rpc.WPParams{Site: site}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

type wpUpdateRequest struct {
	What string `json:"what"`
}

func (s *Server) handleWPUpdate(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !isWordPress(site.Type) {
		respondErr(w, http.StatusBadRequest, "not a WordPress site")
		return
	}
	var req wpUpdateRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	task, err := s.runTask("wp.update", site.ID, func(progress func(int, string)) error {
		progress(30, "Updating "+req.What+" on "+site.Domain)
		if err := s.Agent.Call(rpc.MethodWPUpdate, rpc.WPParams{Site: site, What: req.What}, nil); err != nil {
			return err
		}
		// New code can change output — purge the page cache.
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: site}, nil)
		progress(100, "Update complete")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "wp.update", site.Domain, req.What)
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

type objectCacheRequest struct {
	Enable bool `json:"enable"`
}

func (s *Server) handleWPObjectCache(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !isWordPress(site.Type) {
		respondErr(w, http.StatusBadRequest, "not a WordPress site")
		return
	}
	var req objectCacheRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	task, err := s.runTask("wp.object_cache", site.ID, func(progress func(int, string)) error {
		if req.Enable {
			progress(30, "Enabling Redis object cache (installing Redis if needed)")
			if err := s.ensureRedis(); err != nil {
				return err
			}
		}
		if err := s.Agent.Call(rpc.MethodWPObjectCache, rpc.WPParams{Site: site, Enable: req.Enable}, nil); err != nil {
			return err
		}
		site.Config.ObjectCache = req.Enable
		s.Store.UpdateSite(site)
		progress(100, "Object cache "+map[bool]string{true: "enabled", false: "disabled"}[req.Enable])
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "wp.object_cache", site.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// ensureRedis starts Redis on demand the first time a site needs object
// caching (the installer leaves it disabled to stay lean).
func (s *Server) ensureRedis() error {
	var out map[string]string
	// RestartService both starts and enables via systemd restart semantics.
	return s.Agent.Call(rpc.MethodRestartService, rpc.ServiceParams{Name: "redis"}, &out)
}
