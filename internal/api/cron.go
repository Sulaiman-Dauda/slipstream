package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// A permissive-but-safe cron schedule validator (5 fields of the usual
// character set). Commands are validated for control characters only —
// they run as the unprivileged site user, jailed by that user's rights.
var cronScheduleRe = regexp.MustCompile(`^[\d\*/,\-\s]+$`)

func validCronSchedule(s string) bool {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return false
	}
	return cronScheduleRe.MatchString(s)
}

func (s *Server) handleListCron(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	jobs, err := s.Store.ListCronJobs(site.ID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []state.CronJob{}
	}
	respond(w, http.StatusOK, jobs)
}

type createCronRequest struct {
	Schedule    string `json:"schedule"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

func (s *Server) handleCreateCron(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req createCronRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !validCronSchedule(req.Schedule) {
		respondErr(w, http.StatusBadRequest, "schedule must be 5 cron fields, e.g. */15 * * * *")
		return
	}
	if strings.TrimSpace(req.Command) == "" || strings.ContainsAny(req.Command, "\n\r") {
		respondErr(w, http.StatusBadRequest, "command is required and must be a single line")
		return
	}
	job, err := s.Store.CreateCronJob(state.CronJob{
		SiteID: site.ID, Schedule: req.Schedule, Command: req.Command,
		Description: req.Description, Enabled: true,
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.renderCrontab(site); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "cron.create", site.Domain, req.Schedule+" "+req.Command)
	respond(w, http.StatusCreated, job)
}

func (s *Server) handleDeleteCron(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.Store.GetCronJob(id)
	if err != nil {
		respondErr(w, http.StatusNotFound, "cron job not found")
		return
	}
	if !s.canAccessSite(r, job.SiteID) {
		respondErr(w, http.StatusNotFound, "cron job not found")
		return
	}
	if err := s.Store.DeleteCronJob(id); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Record which site/job was removed -- an empty subject here left the
	// audit trail unable to answer "which cron job did admin X delete?".
	siteDomain := ""
	if site, err := s.Store.GetSite(job.SiteID); err == nil {
		s.renderCrontab(site)
		siteDomain = site.Domain
	}
	s.Store.Audit(s.actor(r), "cron.delete", siteDomain, job.Schedule+" "+job.Command)
	respond(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleRunCron(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job, err := s.Store.GetCronJob(id)
	if err != nil || !s.canAccessSite(r, job.SiteID) {
		respondErr(w, http.StatusNotFound, "cron job not found")
		return
	}
	site, err := s.Store.GetSite(job.SiteID)
	if err != nil {
		respondErr(w, http.StatusNotFound, "site not found")
		return
	}
	var res rpc.RunCronResult
	if err := s.Agent.Call(rpc.MethodRunCron, rpc.RunCronParams{SystemUser: site.SystemUser, Command: job.Command}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.Store.UpdateCronRun(job.ID, res.Status)
	s.Store.Audit(s.actor(r), "cron.run", site.Domain, job.Description)
	respond(w, http.StatusOK, res)
}

// renderCrontab rebuilds a site's entire crontab from desired state and
// installs it through the agent.
func (s *Server) renderCrontab(site state.Site) error {
	jobs, err := s.Store.ListCronJobs(site.ID)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Managed by Slipstream - edit cron jobs in the panel.\n")
	b.WriteString("PATH=/usr/local/bin:/usr/bin:/bin\n")
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		logPath := fmt.Sprintf("%s/logs/cron-%d.log", site.RootPath, j.ID)
		script := fmt.Sprintf("printf '\\n[slipstream start %%s]\\n' \"$(date -Is)\"; { %s; }; status=$?; printf '[slipstream end %%s status=%%s]\\n' \"$(date -Is)\" \"$status\"; exit \"$status\"", j.Command)
		rotate := fmt.Sprintf("status=$?; tail -n 500 %s > %s.tmp && mv %s.tmp %s; exit $status", shellQuote(logPath), shellQuote(logPath), shellQuote(logPath), shellQuote(logPath))
		fmt.Fprintf(&b, "%s /bin/sh -c %s >> %s 2>&1; %s\n", j.Schedule, shellQuote(script), shellQuote(logPath), rotate)
	}
	return s.Agent.Call(rpc.MethodWriteCrontab, rpc.CrontabParams{
		SystemUser: site.SystemUser, Content: b.String(),
	}, nil)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
