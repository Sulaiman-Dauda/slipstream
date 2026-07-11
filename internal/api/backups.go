package api

import (
	"errors"
	"net/http"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// backupConfig resolves the panel-wide Restic repository settings.
func (s *Server) backupConfig(w http.ResponseWriter) (repo, password string, ok bool) {
	repo, _ = s.Store.GetSetting("backup_repository", "")
	password, _ = s.Store.GetSetting("backup_password", "")
	if repo == "" || password == "" {
		respondErr(w, http.StatusPreconditionFailed, "configure backup_repository and backup_password in settings first")
		return "", "", false
	}
	return repo, password, true
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	backups, err := s.Store.ListBackups(site.ID, 50)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if backups == nil {
		backups = []state.Backup{}
	}
	respond(w, http.StatusOK, backups)
}

func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	repo, password, ok := s.backupConfig(w)
	if !ok {
		return
	}
	s.Store.Audit(s.actor(r), "backup.run", site.Domain, "")
	task, err := s.runTask("backup.run", site.ID, func(progress func(int, string)) error {
		progress(20, "Snapshotting "+site.Domain+" (files + database)")
		var res rpc.BackupResult
		if err := s.Agent.Call(rpc.MethodRunBackup, rpc.BackupParams{
			Site: site, Repository: repo, Password: password, Kind: "full",
		}, &res); err != nil {
			return err
		}
		if _, err := s.Store.CreateBackup(state.Backup{
			SiteID: site.ID, SnapshotID: res.SnapshotID, Repository: repo,
			SizeBytes: res.SizeBytes, Kind: "full",
		}); err != nil {
			return err
		}
		progress(100, "Snapshot "+res.SnapshotID+" stored off-site")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// handleTestBackupRepo checks the configured Restic repository is reachable
// so operators can validate backups before depending on them.
func (s *Server) handleTestBackupRepo(w http.ResponseWriter, r *http.Request) {
	repo, password, ok := s.backupConfig(w)
	if !ok {
		return
	}
	var out map[string]any
	if err := s.Agent.Call(rpc.MethodTestBackup, rpc.BackupParams{Repository: repo, Password: password}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, "repository not reachable: "+err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "backup.test", repo, "")
	respond(w, http.StatusOK, out)
}

// handleVerifyBackup runs the verified-restore test: restore into a scratch
// directory, check integrity, record the measured recovery time.
func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	backup, err := s.Store.GetBackup(id)
	if errors.Is(err, state.ErrNotFound) {
		respondErr(w, http.StatusNotFound, "backup not found")
		return
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	site, err := s.Store.GetSite(backup.SiteID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, password, ok := s.backupConfig(w)
	if !ok {
		return
	}
	s.Store.Audit(s.actor(r), "backup.verify", site.Domain, backup.SnapshotID)
	task, err := s.runTask("backup.verify", site.ID, func(progress func(int, string)) error {
		progress(20, "Restoring "+backup.SnapshotID+" into a scratch environment")
		var res rpc.VerifyResult
		if err := s.Agent.Call(rpc.MethodVerifyBackup, rpc.RestoreParams{
			Site: site, Repository: backup.Repository, Password: password, SnapshotID: backup.SnapshotID,
		}, &res); err != nil {
			return err
		}
		status := state.VerifyFailed
		if res.Passed {
			status = state.VerifyPassed
		}
		if err := s.Store.SetBackupVerified(backup.ID, status, res.RestoreMillis); err != nil {
			return err
		}
		if !res.Passed {
			return errors.New("restore test failed: " + res.Detail)
		}
		progress(100, "Restore test passed: "+res.Detail)
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

