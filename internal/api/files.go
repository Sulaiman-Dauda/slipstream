package api

import (
	"net/http"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	var res rpc.ListFilesResult
	if err := s.Agent.Call(rpc.MethodListFiles, rpc.ListFilesParams{Site: site, RelPath: rel}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	var res rpc.ReadFileResult
	if err := s.Agent.Call(rpc.MethodReadFile, rpc.ReadFileParams{Site: site, RelPath: rel}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

type writeFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req writeFileRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	var out map[string]string
	if err := s.Agent.Call(rpc.MethodWriteFile, rpc.WriteFileParams{Site: site, RelPath: req.Path, Content: req.Content}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "file.write", site.Domain, req.Path)
	respond(w, http.StatusOK, out)
}

type sftpRequest struct {
	Enable   bool   `json:"enable"`
	Password string `json:"password"`
}

func (s *Server) handleSetSFTP(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req sftpRequest
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if req.Enable && len(req.Password) < 12 {
		respondErr(w, http.StatusBadRequest, "SFTP password must be at least 12 characters")
		return
	}
	var out map[string]string
	if err := s.Agent.Call(rpc.MethodSetSFTP, rpc.SFTPParams{SystemUser: site.SystemUser, Password: req.Password, Enable: req.Enable}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	site.Config.SFTPEnabled = req.Enable
	s.Store.UpdateSite(site)
	s.Store.Audit(s.actor(r), "sftp.set", site.Domain, map[bool]string{true: "enabled", false: "disabled"}[req.Enable])
	respond(w, http.StatusOK, map[string]any{
		"sftp": out, "host": r.Host, "username": site.SystemUser, "port": 22,
	})
}
