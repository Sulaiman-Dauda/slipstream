package api

import (
	"net/http"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

type phpSettingsRequest struct {
	PHPVersion          *string `json:"php_version"`
	MemoryLimitMB       *int    `json:"memory_limit_mb"`
	UploadMaxMB         *int    `json:"upload_max_mb"`
	MaxExecutionSeconds *int    `json:"max_execution_seconds"`
}

// Curated PHP settings — bounded ranges, not a raw php.ini editor, so the
// single-source-of-truth and drift model stay intact.
func (s *Server) handlePHPSettings(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if site.PHPVersion == "" {
		respondErr(w, http.StatusBadRequest, "this site does not run PHP")
		return
	}
	previousPHP := site.Config.PHP
	previousVersion := site.PHPVersion
	var req phpSettingsRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.PHPVersion != nil {
		if !supportedPHP[*req.PHPVersion] {
			respondErr(w, http.StatusBadRequest, "unsupported PHP version")
			return
		}
		site.PHPVersion = *req.PHPVersion
	}
	if req.MemoryLimitMB != nil {
		if *req.MemoryLimitMB < 64 || *req.MemoryLimitMB > 4096 {
			respondErr(w, http.StatusBadRequest, "memory limit must be 64–4096 MB")
			return
		}
		site.Config.PHP.MemoryLimitMB = *req.MemoryLimitMB
	}
	if req.UploadMaxMB != nil {
		if *req.UploadMaxMB < 1 || *req.UploadMaxMB > 2048 {
			respondErr(w, http.StatusBadRequest, "upload limit must be 1–2048 MB")
			return
		}
		site.Config.PHP.UploadMaxMB = *req.UploadMaxMB
	}
	if req.MaxExecutionSeconds != nil {
		if *req.MaxExecutionSeconds < 10 || *req.MaxExecutionSeconds > 600 {
			respondErr(w, http.StatusBadRequest, "execution time must be 10–600 seconds")
			return
		}
		site.Config.PHP.MaxExecutionSeconds = *req.MaxExecutionSeconds
	}
	if err := s.Store.UpdateSite(site); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, err := s.runTask("site.php", site.ID, func(progress func(int, string)) error {
		progress(40, "Applying PHP settings for "+site.Domain)
		var res rpc.ApplyResult
		if err := s.Agent.Call(rpc.MethodApplySiteConfig, rpc.SiteRef{Site: site}, &res); err != nil {
			// The agent never applied the new version/limits (e.g. a php-fpm
			// unit that doesn't exist on this host) -- revert the stored
			// record so it doesn't claim settings that were never actually
			// applied. Without this, GetSite reports the requested PHP
			// version indefinitely while the site keeps running the old one.
			reverted := site
			reverted.Config.PHP = previousPHP
			reverted.PHPVersion = previousVersion
			s.Store.UpdateSite(reverted)
			return err
		}
		for _, f := range res.Files {
			s.Store.RecordManagedFile(f.Path, f.SHA256)
		}
		progress(100, "PHP settings applied")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "site.php", site.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"site": site, "task": task})
}

var supportedPHP = map[string]bool{"8.2": true, "8.3": true, "8.4": true, "8.5": true}
