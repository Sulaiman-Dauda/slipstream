package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

func (s *Server) handleImportMigration(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Archive   string `json:"archive"`
		SQL       string `json:"sql"`
		OldDomain string `json:"old_domain"`
		Confirm   string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Confirm != site.Domain {
		respondErr(w, http.StatusBadRequest, "type the destination domain to confirm migration")
		return
	}
	if strings.TrimSpace(req.Archive) == "" {
		respondErr(w, http.StatusBadRequest, "site archive path is required")
		return
	}
	if req.SQL != "" && (!site.Config.Database.Enabled || site.Config.Database.External) {
		respondErr(w, http.StatusPreconditionFailed, "SQL import requires a local managed database")
		return
	}
	releaseID := time.Now().UTC().Format("20060102-150405")
	s.Store.Audit(s.actor(r), "migration.import", site.Domain, req.Archive)
	task, err := s.runTask("migration.import", site.ID, func(progress func(int, string)) error {
		progress(10, "Validating and extracting migration archive")
		var res rpc.MigrationResult
		if err := s.Agent.Call(rpc.MethodImportMigration, rpc.MigrationParams{
			Site: site, Archive: req.Archive, SQL: req.SQL,
			OldDomain: strings.ToLower(strings.TrimSpace(req.OldDomain)), ReleaseID: releaseID,
		}, &res); err != nil {
			return err
		}
		progress(85, "Recording immutable migration release")
		_, err := s.Store.CreateDeployment(state.Deployment{
			SiteID: site.ID, ReleaseID: res.ReleaseID,
			Path:   site.RootPath + "/releases/" + res.ReleaseID,
			Status: state.DeployPromoted,
		})
		if err != nil {
			return err
		}
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: site}, nil)
		s.warmInBackground(site)
		done := fmt.Sprintf("Migration imported and activated (%d files)", res.Files)
		if res.Skipped > 0 {
			done += fmt.Sprintf("; %d unsafe or special entries skipped", res.Skipped)
		}
		progress(100, done)
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task, "release_id": releaseID})
}
