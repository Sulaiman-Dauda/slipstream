package api

import (
	"context"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// StartScheduler runs the panel's background maintenance loop: scheduled
// backups, certificate renewal, and cleanup of expired database consoles.
// It ticks every few minutes; each job tracks its own last-run so a restart
// never double-runs or skips.
func (s *Server) StartScheduler(ctx context.Context) {
	go func() {
		// A short initial delay lets the agent finish coming up.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.runSchedulerTick()
				timer.Reset(5 * time.Minute)
			}
		}
	}()
}

func (s *Server) runSchedulerTick() {
	defer func() {
		if rec := recover(); rec != nil {
			s.Log.Error("scheduler tick panicked", "panic", rec)
		}
	}()
	s.Store.PurgeExpiredAdminerSessions()
	s.collectMetricSample()
	s.runScheduledBackups()
	s.renewCertificatesIfDue()
}

func (s *Server) collectMetricSample() {
	var res rpc.SystemStatusResult
	if err := s.Agent.Call(rpc.MethodSystemStatus, nil, &res); err != nil {
		return
	}
	_ = s.Store.RecordMetric(state.MetricSample{
		CPUHeadroomPct: res.CPUHeadroomPct, MemHeadroomPct: res.MemHeadroomPct,
		DiskHeadroomPct: res.DiskHeadroomPct, Load1: res.Load1,
	})
}

// dueInterval maps a policy schedule to a minimum spacing between backups.
func dueInterval(schedule string) time.Duration {
	switch schedule {
	case "hourly":
		return time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	default: // daily
		return 24 * time.Hour
	}
}

func (s *Server) runScheduledBackups() {
	repo, _ := s.Store.GetSetting("backup_repository", "")
	password, _ := s.Store.GetSetting("backup_password", "")
	if repo == "" || password == "" {
		return // backups not configured
	}
	sites, err := s.Store.ListSites()
	if err != nil {
		return
	}
	for _, site := range sites {
		if site.Status != state.SiteActive || !site.Config.Backups.Enabled || site.StagingOf != 0 {
			continue
		}
		key := "backup.site." + itoa(site.ID)
		last, _ := s.Store.LastScheduleRun(key)
		if time.Since(last) < dueInterval(site.Config.Backups.Schedule) {
			continue
		}
		// Reserve the slot before starting so the next 5-minute tick does not
		// launch a second backup while this one is still running.
		s.Store.MarkScheduleRun(key)
		site := site
		s.runTask("backup.scheduled", site.ID, func(progress func(int, string)) error {
			progress(20, "Scheduled backup of "+site.Domain)
			var res rpc.BackupResult
			if err := s.Agent.Call(rpc.MethodRunBackup, rpc.BackupParams{
				Site: site, Repository: repo, Password: password, Kind: "full",
			}, &res); err != nil {
				// Clear the slot so it retries next tick instead of waiting a
				// full interval after a failure.
				s.Store.ClearScheduleRun(key)
				return err
			}
			s.Store.CreateBackup(state.Backup{
				SiteID: site.ID, SnapshotID: res.SnapshotID, Repository: repo,
				SizeBytes: res.SizeBytes, Kind: "full",
			})
			progress(100, "Snapshot "+res.SnapshotID+" stored")
			return nil
		})
	}
}

// renewCertificatesIfDue renews site and panel certs roughly monthly;
// certbot itself is idempotent and only renews when within 30 days of
// expiry, so calling often is safe.
func (s *Server) renewCertificatesIfDue() {
	key := "cert.renew"
	last, _ := s.Store.LastScheduleRun(key)
	if time.Since(last) < 12*time.Hour {
		return
	}
	s.Store.MarkScheduleRun(key)

	email, _ := s.Store.GetSetting("acme_email", "")
	if email == "" {
		return
	}
	sites, err := s.Store.ListSites()
	if err != nil {
		return
	}
	for _, site := range sites {
		if site.Status != state.SiteActive || site.Type == state.SiteProxy {
			continue
		}
		site := site
		// IssueCertificate is renew-or-issue; certbot no-ops if not due.
		var res rpc.ApplyResult
		if err := s.Agent.Call(rpc.MethodIssueCertificate, rpc.CertificateParams{Site: site, Email: email}, &res); err == nil {
			for _, f := range res.Files {
				s.Store.RecordManagedFile(f.Path, f.SHA256)
			}
		}
	}

	// Panel certificate renewal.
	if domain, _ := s.Store.GetSetting("panel_domain", ""); domain != "" {
		port := s.PanelPort
		if port == 0 {
			port = 5252
		}
		s.Agent.Call(rpc.MethodPanelCert, rpc.PanelCertParams{Domain: domain, Email: email, Port: port}, nil)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
