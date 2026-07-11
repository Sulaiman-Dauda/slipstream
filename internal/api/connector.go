package api

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

type connectorPurgeRequest struct {
	URLs []string `json:"urls"`
	All  bool     `json:"all"`
}

// handleConnectorPurge receives precise invalidation events from the
// WordPress connector. Authentication is a per-site bearer token, and a
// site's token can only ever purge that site's own cache — URL hosts are
// checked against the site's domains.
func (s *Server) handleConnectorPurge(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		respondErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	site, ok := s.siteForConnectorToken(token)
	if !ok {
		respondErr(w, http.StatusUnauthorized, "invalid token")
		return
	}

	var req connectorPurgeRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}

	params := rpc.PurgeParams{Site: site}
	if !req.All {
		allowed := map[string]bool{site.Domain: true}
		for _, a := range site.Aliases {
			allowed[a] = true
		}
		for _, raw := range req.URLs {
			u, err := url.Parse(raw)
			if err != nil || !allowed[u.Hostname()] {
				continue // silently drop foreign or malformed URLs
			}
			params.URLs = append(params.URLs, raw)
		}
		if len(params.URLs) == 0 {
			respond(w, http.StatusOK, rpc.PurgeResult{Removed: 0})
			return
		}
	}
	var res rpc.PurgeResult
	if err := s.Agent.Call(rpc.MethodPurgeCache, params, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) siteForConnectorToken(token string) (state.Site, bool) {
	sites, err := s.Store.ListSites()
	if err != nil {
		return state.Site{}, false
	}
	for _, site := range sites {
		stored, _ := s.Store.GetSetting(secretKey(site.ID, "connector_token"), "")
		if stored != "" && subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1 {
			return site, true
		}
	}
	return state.Site{}, false
}
