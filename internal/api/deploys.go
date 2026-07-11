package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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

		progress(55, "Comparing performance")
		report := guard.Compare(baseline, candidate, guard.DefaultThresholds())
		reportJSON, _ := json.Marshal(report)

		if report.Verdict == guard.VerdictBlock || (report.Verdict == guard.VerdictWarn && !req.Force) {
			dep, _ := s.Store.CreateDeployment(state.Deployment{
				SiteID: prod.ID, ReleaseID: time.Now().UTC().Format("20060102-150405"),
				Path: "", Status: state.DeployBlocked, GuardJSON: string(reportJSON),
			})
			_ = dep
			for _, reason := range report.Reasons {
				progress(60, reason)
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
		s.warmInBackground(prod)
		progress(100, "Safe push complete: "+releaseID+" is live")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}
