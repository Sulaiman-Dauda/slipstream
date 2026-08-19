package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/guard"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	deps, err := s.Store.ListDeployments(site.ID, 50)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deps == nil {
		deps = []state.Deployment{}
	}
	respond(w, http.StatusOK, deps)
}

type deployRequest struct {
	SourceDir string `json:"source_dir"`
}

// handleDeploy stages a new immutable release from a directory on the
// server (uploaded, git-checked-out, or staging's current release).
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req deployRequest
	if err := decode(r, &req); err != nil || req.SourceDir == "" {
		respondErr(w, http.StatusBadRequest, "source_dir required")
		return
	}
	if !filepath.IsAbs(req.SourceDir) {
		respondErr(w, http.StatusBadRequest, "source_dir must be absolute")
		return
	}
	releaseID := time.Now().UTC().Format("20060102-150405")
	s.Store.Audit(s.actor(r), "deploy.create", site.Domain, releaseID)

	task, err := s.runTask("deploy.create", site.ID, func(progress func(int, string)) error {
		progress(20, "Creating release "+releaseID)
		var res rpc.DeployResult
		if err := s.Agent.Call(rpc.MethodDeployRelease, rpc.DeployParams{
			Site: site, SourceDir: req.SourceDir, ReleaseID: releaseID,
		}, &res); err != nil {
			return err
		}
		_, err := s.Store.CreateDeployment(state.Deployment{
			SiteID: site.ID, ReleaseID: res.ReleaseID, Path: res.Path,
			Checksum: res.Checksum, Status: state.DeployCreated,
		})
		if err != nil {
			return err
		}
		progress(100, "Release "+releaseID+" staged (not yet live)")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task, "release_id": releaseID})
}

// handlePromote makes a staged release live, then purges the page cache.
func (s *Server) handlePromote(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dep, err := s.Store.GetDeployment(id)
	if errors.Is(err, state.ErrNotFound) {
		respondErr(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	site, err := s.Store.GetSite(dep.SiteID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.canAccessSite(r, site.ID) {
		respondErr(w, http.StatusNotFound, "deployment not found")
		return
	}
	s.Store.Audit(s.actor(r), "deploy.promote", site.Domain, dep.ReleaseID)

	task, err := s.runTask("deploy.promote", site.ID, func(progress func(int, string)) error {
		progress(40, "Promoting "+dep.ReleaseID)
		if err := s.Agent.Call(rpc.MethodPromoteRelease, rpc.ReleaseParams{Site: site, ReleaseID: dep.ReleaseID}, nil); err != nil {
			return err
		}
		s.Store.SetDeploymentStatus(dep.ID, state.DeployPromoted, "")
		progress(80, "Purging page cache")
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: site}, nil)
		progress(90, "Warming cache so the first visitor is not cold")
		s.warmInBackground(site)
		progress(100, dep.ReleaseID+" is live")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// handleRollback instantly repoints production at the previous release.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	s.Store.Audit(s.actor(r), "deploy.rollback", site.Domain, "")
	task, err := s.runTask("deploy.rollback", site.ID, func(progress func(int, string)) error {
		progress(40, "Rolling back "+site.Domain)
		var res map[string]string
		if err := s.Agent.Call(rpc.MethodRollbackRelease, rpc.ReleaseParams{Site: site}, &res); err != nil {
			return err
		}
		// Mark the most recent promoted deployment as rolled back.
		if deps, err := s.Store.ListDeployments(site.ID, 10); err == nil {
			for _, d := range deps {
				if d.Status == state.DeployPromoted {
					s.Store.SetDeploymentStatus(d.ID, state.DeployRolledBack, "")
					break
				}
			}
		}
		progress(80, "Purging page cache")
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: site}, nil)
		progress(100, "Now serving "+res["current"])
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

type safePushRequest struct {
	// Paths to probe; defaults to guard.DefaultProbePaths.
	Paths []string `json:"paths"`
	// Force promotes even on a warn verdict (never on block).
	Force bool `json:"force"`
}

func protectedPromotionTable(site state.Site, table string) bool {
	t := strings.ToLower(table)
	if strings.HasSuffix(t, "_users") || strings.HasSuffix(t, "_usermeta") {
		return true
	}
	if site.Type != state.SiteWooCommerce {
		return false
	}
	for _, suffix := range []string{"_posts", "_postmeta", "_comments", "_commentmeta", "_woocommerce_sessions"} {
		if strings.HasSuffix(t, suffix) {
			return true
		}
	}
	return strings.Contains(t, "_actionscheduler_") || strings.Contains(t, "_wc_order") || strings.Contains(t, "_woocommerce_order")
}

func (s *Server) handleStagingTables(w http.ResponseWriter, r *http.Request) {
	prod, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	stg, err := s.Store.StagingSiteFor(prod.ID)
	if err != nil {
		respondErr(w, http.StatusPreconditionFailed, "create a staging site first")
		return
	}
	var res rpc.DBQueryResult
	if err := s.Agent.Call(rpc.MethodDBQuery, rpc.DBQueryParams{Database: stg.Config.Database.Name, SQL: "SHOW TABLES"}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	tables := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) > 0 {
			tables = append(tables, map[string]any{"name": row[0], "protected": protectedPromotionTable(prod, row[0])})
		}
	}
	respond(w, http.StatusOK, map[string]any{"tables": tables, "staging": stg.Domain})
}

func (s *Server) handlePushStagingDatabase(w http.ResponseWriter, r *http.Request) {
	prod, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	stg, err := s.Store.StagingSiteFor(prod.ID)
	if err != nil {
		respondErr(w, http.StatusPreconditionFailed, "create a staging site first")
		return
	}
	var req struct {
		Tables  []string `json:"tables"`
		Confirm string   `json:"confirm"`
	}
	if err := decode(r, &req); err != nil || req.Confirm != prod.Domain {
		respondErr(w, http.StatusBadRequest, "type the production domain to confirm database promotion")
		return
	}
	if len(req.Tables) == 0 || len(req.Tables) > 50 {
		respondErr(w, http.StatusBadRequest, "select between 1 and 50 tables")
		return
	}
	for _, table := range req.Tables {
		if protectedPromotionTable(prod, table) {
			respondErr(w, http.StatusBadRequest, "selection contains protected live data table "+table)
			return
		}
	}
	repo, password, ok := s.backupConfig(w)
	if !ok {
		return
	}
	s.Store.Audit(s.actor(r), "staging.database-push", prod.Domain, strings.Join(req.Tables, ","))
	task, err := s.runTask("staging.database-push", prod.ID, func(progress func(int, string)) error {
		progress(10, "Creating off-site production safety snapshot")
		var safety rpc.BackupResult
		if err := s.Agent.Call(rpc.MethodRunBackup, rpc.BackupParams{Site: prod, Repository: repo, Password: password, Kind: "full"}, &safety); err != nil {
			return fmt.Errorf("safety snapshot failed; production was not changed: %w", err)
		}
		if _, err := s.Store.CreateBackup(state.Backup{SiteID: prod.ID, SnapshotID: safety.SnapshotID, Repository: repo, SizeBytes: safety.SizeBytes, Kind: "pre-database-push"}); err != nil {
			return err
		}
		progress(45, fmt.Sprintf("Promoting %d selected tables", len(req.Tables)))
		var out map[string]any
		if err := s.Agent.Call(rpc.MethodSyncStagingDB, rpc.SyncStagingDBParams{Production: prod, Staging: stg, Tables: req.Tables}, &out); err != nil {
			return err
		}
		progress(90, "Purging production cache")
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: prod}, nil)
		progress(100, "Selected database tables promoted; safety snapshot "+safety.SnapshotID+" retained")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// handleSafePush is the flagship flow: measure staging against production
// with Performance Guard, and only promote staging's code if it does not
// regress. A blocked push leaves production untouched.
func (s *Server) handleSafePush(w http.ResponseWriter, r *http.Request) {
	prod, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	stg, err := s.Store.StagingSiteFor(prod.ID)
	if errors.Is(err, state.ErrNotFound) {
		respondErr(w, http.StatusPreconditionFailed, "create a staging site first")
		return
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var req safePushRequest
	decode(r, &req)
	paths := req.Paths
	if len(paths) == 0 {
		paths = guard.DefaultProbePaths
	}
	probeTarget, _ := s.Store.GetSetting("probe_target", "https://127.0.0.1")
	s.Store.Audit(s.actor(r), "deploy.safe-push", prod.Domain, "")

	task, err := s.runTask("deploy.safe-push", prod.ID, func(progress func(int, string)) error {
		ctx := context.Background()

		progress(10, "Measuring production baseline")
		baseProber := &guard.Prober{Target: probeTarget, Host: prod.Domain}
		baseline, err := baseProber.Measure(ctx, paths)
		if err != nil {
			return fmt.Errorf("baseline measurement: %w", err)
		}

		progress(35, "Measuring staging candidate")
		candProber := &guard.Prober{Target: probeTarget, Host: stg.Domain}
		candidate, err := candProber.Measure(ctx, paths)
		if err != nil {
			return fmt.Errorf("candidate measurement: %w", err)
		}

		// Re-measure production. Guard can only attribute a difference to the
		// candidate if production behaved the same either side of it; on a
		// contended box the staging clone slows production too and the change
		// takes the blame for the machine.
		progress(50, "Re-measuring production baseline")
		recheck, err := baseProber.Measure(ctx, paths)
		if err != nil {
			return fmt.Errorf("baseline recheck: %w", err)
		}

		progress(55, "Comparing performance")
		thresholds := guard.DefaultThresholds()
		report := guard.Compare(baseline, candidate, thresholds)
		if drift := guard.BaselineDrift(baseline, recheck, thresholds); len(drift) > 0 {
			report = guard.Inconclusive(baseline, candidate, thresholds, drift)
		}
		reportJSON, _ := json.Marshal(report)

		if report.Verdict == guard.VerdictBlock ||
			((report.Verdict == guard.VerdictWarn || report.Verdict == guard.VerdictInconclusive) && !req.Force) {
			dep, _ := s.Store.CreateDeployment(state.Deployment{
				SiteID: prod.ID, ReleaseID: time.Now().UTC().Format("20060102-150405"),
				Path: "", Status: state.DeployBlocked, GuardJSON: string(reportJSON),
			})
			_ = dep
			for _, reason := range report.Reasons {
				progress(60, reason)
			}
			if report.Verdict == guard.VerdictInconclusive {
				return fmt.Errorf("performance guard: inconclusive, the server was not stable enough to measure — promotion refused (retry when the box is quiet, or force to override)")
			}
			return fmt.Errorf("performance guard verdict: %s — promotion refused", report.Verdict)
		}

		progress(65, "Guard verdict: "+string(report.Verdict)+" — deploying staging code to production")
		releaseID := time.Now().UTC().Format("20060102-150405")
		var deployRes rpc.DeployResult
		if err := s.Agent.Call(rpc.MethodDeployRelease, rpc.DeployParams{
			Site: prod, SourceDir: filepath.Join(stg.RootPath, "current"), ReleaseID: releaseID,
		}, &deployRes); err != nil {
			return err
		}
		dep, err := s.Store.CreateDeployment(state.Deployment{
			SiteID: prod.ID, ReleaseID: releaseID, Path: deployRes.Path,
			Checksum: deployRes.Checksum, Status: state.DeployGuarding, GuardJSON: string(reportJSON),
		})
		if err != nil {
			return err
		}

		progress(85, "Promoting "+releaseID)
		if err := s.Agent.Call(rpc.MethodPromoteRelease, rpc.ReleaseParams{Site: prod, ReleaseID: releaseID}, nil); err != nil {
			return err
		}
		s.Store.SetDeploymentStatus(dep.ID, state.DeployPromoted, "")
		s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: prod}, nil)

		progress(90, "Verifying production after promotion")
		liveProber := &guard.Prober{Target: probeTarget, Host: prod.Domain}
		live, liveErr := liveProber.Measure(ctx, paths)
		if liveErr != nil {
			if rollbackErr := s.Agent.Call(rpc.MethodRollbackRelease, rpc.ReleaseParams{Site: prod}, nil); rollbackErr != nil {
				return fmt.Errorf("post-promotion verification failed (%v) and automatic rollback failed: %w", liveErr, rollbackErr)
			}
			s.Store.SetDeploymentStatus(dep.ID, state.DeployRolledBack, "")
			s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: prod}, nil)
			return fmt.Errorf("post-promotion verification failed; automatically rolled back: %w", liveErr)
		}
		liveReport := guard.Compare(baseline, live, guard.DefaultThresholds())
		liveJSON, _ := json.Marshal(liveReport)
		if liveReport.Verdict == guard.VerdictBlock {
			progress(94, "Production regression detected — rolling back automatically")
			if rollbackErr := s.Agent.Call(rpc.MethodRollbackRelease, rpc.ReleaseParams{Site: prod}, nil); rollbackErr != nil {
				return fmt.Errorf("production regressed and automatic rollback failed: %w", rollbackErr)
			}
			s.Store.SetDeploymentStatus(dep.ID, state.DeployRolledBack, string(liveJSON))
			s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: prod}, nil)
			return fmt.Errorf("production regression detected; automatically rolled back: %s", strings.Join(liveReport.Reasons, "; "))
		}
		s.Store.SetDeploymentStatus(dep.ID, state.DeployPromoted, string(liveJSON))
		s.warmInBackground(prod)
		progress(100, "Safe push verified: "+releaseID+" is live")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}
