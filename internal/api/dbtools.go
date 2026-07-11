package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

// Database console: read-only by default. Write statements are gated behind
// an explicit allow_writes flag so an accidental DELETE cannot slip out of
// a "run this SELECT" habit.
var readOnlyPrefixes = []string{"select", "show", "describe", "desc", "explain", "with"}

func isReadOnlySQL(sql string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(sql))
	for _, p := range readOnlyPrefixes {
		if strings.HasPrefix(trimmed, p+" ") || trimmed == p {
			return true
		}
	}
	return false
}

func (s *Server) handleDatabaseInfo(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !site.Config.Database.Enabled {
		respondErr(w, http.StatusPreconditionFailed, "this site has no managed database")
		return
	}
	// Table list + sizes via information_schema.
	var res rpc.DBQueryResult
	err := s.Agent.Call(rpc.MethodDBQuery, rpc.DBQueryParams{
		Database: site.Config.Database.Name,
		SQL:      "SELECT table_name AS 'Table', table_rows AS 'Rows', ROUND(data_length/1024/1024,2) AS 'Data MB' FROM information_schema.tables WHERE table_schema=DATABASE() ORDER BY data_length DESC;",
	}, &res)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"database": site.Config.Database.Name,
		"user":     site.Config.Database.User,
		"tables":   res,
	})
}

type dbQueryRequest struct {
	SQL         string `json:"sql"`
	AllowWrites bool   `json:"allow_writes"`
}

func (s *Server) handleDatabaseQuery(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !site.Config.Database.Enabled {
		respondErr(w, http.StatusPreconditionFailed, "this site has no managed database")
		return
	}
	var req dbQueryRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !req.AllowWrites && !isReadOnlySQL(req.SQL) {
		respondErr(w, http.StatusBadRequest, "only read queries are allowed unless you enable write mode")
		return
	}
	var res rpc.DBQueryResult
	if err := s.Agent.Call(rpc.MethodDBQuery, rpc.DBQueryParams{Database: site.Config.Database.Name, SQL: req.SQL}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if req.AllowWrites {
		s.Store.Audit(s.actor(r), "database.write", site.Domain, firstLine(req.SQL))
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) handleDatabaseExport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !site.Config.Database.Enabled {
		respondErr(w, http.StatusPreconditionFailed, "this site has no managed database")
		return
	}
	task, err := s.runTask("database.export", site.ID, func(progress func(int, string)) error {
		progress(30, "Exporting "+site.Config.Database.Name)
		var res rpc.DBExportResult
		if err := s.Agent.Call(rpc.MethodDBExport, rpc.DBExportParams{Site: site, Database: site.Config.Database.Name}, &res); err != nil {
			return err
		}
		progress(100, "Exported to shared/exports (browse via Files)")
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "database.export", site.Domain, "")
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

func (s *Server) handleDatabaseImport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !site.Config.Database.Enabled || site.Config.Database.External {
		respondErr(w, http.StatusPreconditionFailed, "this site has no local managed database")
		return
	}
	var req struct {
		Path    string `json:"path"`
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Confirm != site.Domain {
		respondErr(w, http.StatusBadRequest, "type the site domain to confirm database replacement")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		respondErr(w, http.StatusBadRequest, "SQL file path is required")
		return
	}
	task, err := s.runTask("database.import", site.ID, func(progress func(int, string)) error {
		progress(15, "Creating database rollback point")
		var res rpc.DBImportResult
		if err := s.Agent.Call(rpc.MethodDBImport, rpc.DBImportParams{
			Site: site, Database: site.Config.Database.Name, RelPath: req.Path,
		}, &res); err != nil {
			return err
		}
		progress(100, "Database imported from "+req.Path)
		return nil
	})
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "database.import", site.Domain, req.Path)
	respond(w, http.StatusAccepted, map[string]any{"task": task})
}

// handleLaunchAdminer creates a 30-minute tokenized database console.
func (s *Server) handleLaunchAdminer(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	if !site.Config.Database.Enabled {
		respondErr(w, http.StatusPreconditionFailed, "this site has no managed database")
		return
	}
	dbPassword, _ := s.Store.GetSetting(secretKey(site.ID, "db_password"), "")
	if dbPassword == "" {
		respondErr(w, http.StatusInternalServerError, "database password not found for this site")
		return
	}
	token := randomToken(24)
	expiry := time.Now().Add(30 * time.Minute)
	s.Store.CreateAdminerSession(token, site.ID, 30*time.Minute)

	var res rpc.AdminerResult
	if err := s.Agent.Call(rpc.MethodLaunchAdminer, rpc.AdminerParams{
		Site: site, Token: token, DBName: site.Config.Database.Name,
		DBUser: site.Config.Database.User, DBPassword: dbPassword, ExpiryUnix: expiry.Unix(),
	}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "database.adminer", site.Domain, "")
	respond(w, http.StatusOK, map[string]any{"url": res.URL, "expires_at": expiry})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
