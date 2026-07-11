package api

import (
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/slipstream-panel/slipstream/internal/engine/nginx"
	"github.com/slipstream-panel/slipstream/internal/rpc"
	"github.com/slipstream-panel/slipstream/internal/state"
)

// SitesRoot mirrors the agent's default site layout.
const SitesRoot = "/srv/sites"

type createSiteRequest struct {
	Domain        string   `json:"domain"`
	Aliases       []string `json:"aliases"`
	Type          string   `json:"type"`
	Profile       string   `json:"profile"`
	PHPVersion    string   `json:"php_version"`
	Title         string   `json:"title"`
	AdminEmail    string   `json:"admin_email"`
	AdminUser     string   `json:"admin_user"`
	AdminPassword string   `json:"admin_password"`
	ProxyUpstream string   `json:"proxy_upstream"`
	CacheEnabled  *bool    `json:"cache_enabled"`
	ObjectCache   bool     `json:"object_cache"`
}

var validTypes = map[state.SiteType]bool{
	state.SiteWordPress: true, state.SiteWooCommerce: true, state.SiteStatic: true,
	state.SitePHP: true, state.SiteLaravel: true, state.SiteProxy: true,
}

func defaultProfile(t state.SiteType) state.Profile {
	if t == state.SiteWooCommerce {
		return state.ProfileCommerce
	}
	return state.ProfileBalanced
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	user, _ := s.sessionUser(r)
	var sites []state.Site
	var err error
	if user.Role == "operator" {
		sites, err = s.Store.ListSitesForUser(user.ID)
	} else {
		sites, err = s.Store.ListSites()
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sites == nil {
		sites = []state.Site{}
	}
	respond(w, http.StatusOK, sites)
}

func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	respond(w, http.StatusOK, site)
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var req createSiteRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request: "+err.Error())
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if err := nginx.ValidateDomain(req.Domain); err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for i, a := range req.Aliases {
		req.Aliases[i] = strings.ToLower(strings.TrimSpace(a))
		if err := nginx.ValidateDomain(req.Aliases[i]); err != nil {
			respondErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	siteType := state.SiteType(req.Type)
	if !validTypes[siteType] {
		respondErr(w, http.StatusBadRequest, "invalid site type")
		return
	}
	profile := state.Profile(req.Profile)
	if profile == "" {
		profile = defaultProfile(siteType)
	}
	if profile != state.ProfileBalanced && profile != state.ProfileCommerce && profile != state.ProfileMaximum {
		respondErr(w, http.StatusBadRequest, "invalid profile")
		return
	}
	isWP := siteType == state.SiteWordPress || siteType == state.SiteWooCommerce
	if isWP {
		if _, err := mail.ParseAddress(req.AdminEmail); err != nil {
			respondErr(w, http.StatusBadRequest, "valid admin email required for WordPress")
			return
		}
		if req.AdminUser == "" || len(req.AdminPassword) < 12 {
			respondErr(w, http.StatusBadRequest, "admin user and a 12+ character password required")
			return
		}
	}
	if siteType == state.SiteProxy {
		u, err := url.Parse(req.ProxyUpstream)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			respondErr(w, http.StatusBadRequest, "proxy_upstream must be an http(s) URL")
			return
		}
	}
	phpVersion := req.PHPVersion
	needsPHP := siteType != state.SiteStatic && siteType != state.SiteProxy
	if needsPHP && phpVersion == "" {
		phpVersion = s.DefaultPHP
		if phpVersion == "" {
			phpVersion = "8.4"
		}
	}
	if !needsPHP {
		phpVersion = ""
	}
	cacheEnabled := siteType != state.SiteStatic && siteType != state.SiteProxy
	if req.CacheEnabled != nil {
		cacheEnabled = *req.CacheEnabled && cacheEnabled
	}

	// Laravel serves from public/; the renderer roots the docroot there.
	publicRoot := ""
	if siteType == state.SiteLaravel {
		publicRoot = "public"
	}
	site := state.Site{
		Domain:     req.Domain,
		Aliases:    req.Aliases,
		Type:       siteType,
		Profile:    profile,
		Engine:     state.EngineNginx,
		PHPVersion: phpVersion,
		SystemUser: "pending", // assigned from the record ID below
		RootPath:   filepath.Join(SitesRoot, req.Domain),
		Status:     state.SiteProvisioning,
		Config: state.SiteConfig{
			CacheEnabled:  cacheEnabled,
			ObjectCache:   req.ObjectCache,
			ProxyUpstream: req.ProxyUpstream,
			PublicRoot:    publicRoot,
		},
	}
	created, err := s.Store.CreateSite(site)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			respondErr(w, http.StatusConflict, "a site with this domain already exists")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Identity and database names derive from the immutable record ID.
	created.SystemUser = fmt.Sprintf("slip-site-%d", created.ID)
	if needsPHP {
		created.Config.Database = state.DatabaseConfig{
			Enabled: true, Host: "127.0.0.1", Port: 3306,
			Name: fmt.Sprintf("site_%d", created.ID),
			User: fmt.Sprintf("site_%d", created.ID),
		}
	}
	created.Config.Backups = state.BackupPolicy{Enabled: true, Schedule: "daily", RetentionDays: 30}
	if err := s.Store.UpdateSite(created); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	dbPassword := randomToken(24)
	connectorToken := randomToken(24)
	s.Store.SetSetting(secretKey(created.ID, "db_password"), dbPassword)
	s.Store.SetSetting(secretKey(created.ID, "connector_token"), connectorToken)
	s.Store.Audit(s.actor(r), "site.create", created.Domain, string(created.Type))

	task, err := s.runTask("site.create", created.ID, func(progress func(int, string)) error {
		progress(10, "Provisioning "+created.Domain)
		params := rpc.CreateSiteParams{
			Site:           created,
			DBPassword:     dbPassword,
			ConnectorToken: connectorToken,
		}
		if isWP {
			params.AdminEmail = req.AdminEmail
			params.AdminUser = req.AdminUser
			params.AdminPassword = req.AdminPassword
			params.SiteTitle = req.Title
		}
		var res rpc.CreateSiteResult
		if err := s.Agent.Call(rpc.MethodCreateSite, params, &res); err != nil {
			s.rollbackCreatedSite(created)
			return err
		}
		progress(60, "Configuration rendered, recording state")
		for _, f := range res.Files {
			if err := s.Store.RecordManagedFile(f.Path, f.SHA256); err != nil {
				s.Agent.Call(rpc.MethodDeleteSite, rpc.SiteRef{Site: created}, nil)
				s.rollbackCreatedSite(created)
				return fmt.Errorf("record managed configuration: %w", err)
			}
		}

		if email, _ := s.Store.GetSetting("acme_email", ""); email != "" {
			progress(80, "Requesting Let's Encrypt certificate")
			var certRes rpc.ApplyResult
			if err := s.Agent.Call(rpc.MethodIssueCertificate, rpc.CertificateParams{Site: created, Email: email}, &certRes); err != nil {
				progress(85, "Certificate pending (DNS may not point here yet): "+err.Error())
			} else {
				for _, f := range certRes.Files {
					s.Store.RecordManagedFile(f.Path, f.SHA256)
				}
			}
		}

		created.Status = state.SiteActive
		if err := s.Store.UpdateSite(created); err != nil {
			s.Agent.Call(rpc.MethodDeleteSite, rpc.SiteRef{Site: created}, nil)
			s.rollbackCreatedSite(created)
			return err
		}
		progress(100, created.Domain+" is live")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"site": created, "task": task})
}

func (s *Server) rollbackCreatedSite(site state.Site) {
	for path := range managedFilesForSite(s, site) {
		s.Store.RemoveManagedFile(path)
	}
	s.Store.SetSetting(secretKey(site.ID, "db_password"), "")
	s.Store.SetSetting(secretKey(site.ID, "connector_token"), "")
	s.Store.DeleteSite(site.ID)
}

func (s *Server) markSiteError(id int64) {
	if site, err := s.Store.GetSite(id); err == nil {
		site.Status = state.SiteError
		s.Store.UpdateSite(site)
	}
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	site.Status = state.SiteDeleting
	s.Store.UpdateSite(site)
	s.Store.Audit(s.actor(r), "site.delete", site.Domain, "")

	// Deleting a production site also removes its staging environment, which
	// would otherwise dangle (staging_of pointing at a gone site) along with
	// its on-disk data and secrets.
	var staging *state.Site
	if site.StagingOf == 0 {
		if stg, err := s.Store.StagingSiteFor(site.ID); err == nil {
			staging = &stg
		}
	}

	task, err := s.runTask("site.delete", site.ID, func(progress func(int, string)) error {
		removeOne := func(target state.Site) error {
			if err := s.Agent.Call(rpc.MethodDeleteSite, rpc.SiteRef{Site: target}, nil); err != nil {
				target.Status = state.SiteError
				s.Store.UpdateSite(target)
				return fmt.Errorf("remove %s: %w", target.Domain, err)
			}
			for path := range managedFilesForSite(s, target) {
				s.Store.RemoveManagedFile(path)
			}
			s.Store.SetSetting(secretKey(target.ID, "db_password"), "")
			s.Store.SetSetting(secretKey(target.ID, "connector_token"), "")
			return s.Store.DeleteSite(target.ID)
		}
		if staging != nil {
			progress(15, "Removing staging environment "+staging.Domain)
			if err := removeOne(*staging); err != nil {
				return err
			}
		}
		progress(50, "Removing "+site.Domain)
		if err := removeOne(site); err != nil {
			return err
		}
		progress(100, "Removed")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// siteOwnsPath reports whether a managed file belongs to a site, using exact
// filename/prefix matching (NOT substrings — "slip-site-1" must not match
// "slip-site-10.conf", nor "example.com" match "dev.example.com.conf").
func siteOwnsPath(site state.Site, path string) bool {
	base := filepath.Base(path)
	switch base {
	case site.Domain + ".conf":
		return true
	case fmt.Sprintf("slipstream-cache-%d.conf", site.ID):
		return true
	}
	if site.SystemUser != "" && base == site.SystemUser+".conf" {
		return true
	}
	if site.RootPath != "" && strings.HasPrefix(path, site.RootPath+"/") {
		return true
	}
	return false
}

// managedFilesForSite matches recorded managed files belonging to a site.
func managedFilesForSite(s *Server, site state.Site) map[string]string {
	all, err := s.Store.ManagedFiles()
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for p, h := range all {
		if siteOwnsPath(site, p) {
			out[p] = h
		}
	}
	return out
}

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var body struct {
		URLs []string `json:"urls"`
	}
	// An empty body means "purge everything".
	decode(r, &body)
	var res rpc.PurgeResult
	if err := s.Agent.Call(rpc.MethodPurgeCache, rpc.PurgeParams{Site: site, URLs: body.URLs}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "cache.purge", site.Domain, fmt.Sprintf("%d urls, %d removed", len(body.URLs), res.Removed))
	respond(w, http.StatusOK, res)
}

func (s *Server) handleIssueCertificate(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	email, _ := s.Store.GetSetting("acme_email", "")
	if email == "" {
		respondErr(w, http.StatusPreconditionFailed, "set acme_email in settings first")
		return
	}
	task, err := s.runTask("certificate.issue", site.ID, func(progress func(int, string)) error {
		progress(30, "Requesting certificate for "+site.Domain)
		var res rpc.ApplyResult
		if err := s.Agent.Call(rpc.MethodIssueCertificate, rpc.CertificateParams{Site: site, Email: email}, &res); err != nil {
			return err
		}
		for _, f := range res.Files {
			s.Store.RecordManagedFile(f.Path, f.SHA256)
		}
		progress(100, "Certificate installed")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "certificate.issue", site.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

type updateConfigRequest struct {
	Profile      *string `json:"profile"`
	CacheEnabled *bool   `json:"cache_enabled"`
	CacheTTLSec  *int    `json:"cache_ttl_sec"`
	ObjectCache  *bool   `json:"object_cache"`
	PHPWorkers   *int    `json:"php_workers"`
}

func (s *Server) handleUpdateSiteConfig(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req updateConfigRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Profile != nil {
		p := state.Profile(*req.Profile)
		if p != state.ProfileBalanced && p != state.ProfileCommerce && p != state.ProfileMaximum {
			respondErr(w, http.StatusBadRequest, "invalid profile")
			return
		}
		site.Profile = p
	}
	if req.CacheEnabled != nil {
		site.Config.CacheEnabled = *req.CacheEnabled
	}
	if req.CacheTTLSec != nil && *req.CacheTTLSec >= 0 {
		site.Config.CacheTTLSec = *req.CacheTTLSec
	}
	if req.ObjectCache != nil {
		site.Config.ObjectCache = *req.ObjectCache
	}
	if req.PHPWorkers != nil && *req.PHPWorkers >= 0 && *req.PHPWorkers <= 64 {
		site.Config.Resources.PHPWorkers = *req.PHPWorkers
	}
	if err := s.Store.UpdateSite(site); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, err := s.runTask("site.apply-config", site.ID, func(progress func(int, string)) error {
		progress(40, "Re-rendering configuration for "+site.Domain)
		var res rpc.ApplyResult
		if err := s.Agent.Call(rpc.MethodApplySiteConfig, rpc.SiteRef{Site: site}, &res); err != nil {
			return err
		}
		for _, f := range res.Files {
			s.Store.RecordManagedFile(f.Path, f.SHA256)
		}
		progress(100, "Configuration applied")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "site.config", site.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"site": site, "task": task})
}

func (s *Server) handleCreateStaging(w http.ResponseWriter, r *http.Request) {
	prod, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if prod.StagingOf != 0 {
		respondErr(w, http.StatusBadRequest, "cannot stage a staging site")
		return
	}
	if _, err := s.Store.StagingSiteFor(prod.ID); err == nil {
		respondErr(w, http.StatusConflict, "staging already exists for this site")
		return
	}

	stg := prod
	stg.ID = 0
	stg.Domain = "staging." + prod.Domain
	stg.Aliases = nil
	stg.Status = state.SiteProvisioning
	stg.StagingOf = prod.ID
	created, err := s.Store.CreateSite(stg)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	created.SystemUser = fmt.Sprintf("slip-site-%d", created.ID)
	created.RootPath = filepath.Join(SitesRoot, created.Domain)
	if created.Config.Database.Enabled {
		created.Config.Database.Name = fmt.Sprintf("site_%d", created.ID)
		created.Config.Database.User = fmt.Sprintf("site_%d", created.ID)
	}
	if err := s.Store.UpdateSite(created); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbPassword := randomToken(24)
	connectorToken := randomToken(24)
	s.Store.SetSetting(secretKey(created.ID, "db_password"), dbPassword)
	s.Store.SetSetting(secretKey(created.ID, "connector_token"), connectorToken)
	s.Store.Audit(s.actor(r), "staging.create", prod.Domain, created.Domain)

	task, err := s.runTask("staging.create", prod.ID, func(progress func(int, string)) error {
		progress(20, "Cloning "+prod.Domain+" to "+created.Domain)
		var res rpc.CreateSiteResult
		if err := s.Agent.Call(rpc.MethodCreateStaging, rpc.StagingParams{
			Production: prod, Staging: created, DBPassword: dbPassword, ConnectorToken: connectorToken,
		}, &res); err != nil {
			s.rollbackCreatedSite(created)
			return err
		}
		for _, f := range res.Files {
			s.Store.RecordManagedFile(f.Path, f.SHA256)
		}
		created.Status = state.SiteActive
		if err := s.Store.UpdateSite(created); err != nil {
			return err
		}
		progress(100, created.Domain+" ready")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusAccepted, map[string]any{"staging": created, "task": task})
}

func secretKey(siteID int64, name string) string {
	return fmt.Sprintf("site.%d.%s", siteID, name)
}
