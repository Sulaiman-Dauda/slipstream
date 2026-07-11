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
	if err := s.Store.DeleteCronJob(id); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if site, err := s.Store.GetSite(job.SiteID); err == nil {
		s.renderCrontab(site)
	}
	s.Store.Audit(s.actor(r), "cron.delete", "", "")
	respond(w, http.StatusOK, map[string]string{"status": "deleted"})
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
		fmt.Fprintf(&b, "%s %s\n", j.Schedule, j.Command)
	}
	return s.Agent.Call(rpc.MethodWriteCrontab, rpc.CrontabParams{
		SystemUser: site.SystemUser, Content: b.String(),
	}, nil)
}
