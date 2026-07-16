package api

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/slipstream-panel/slipstream/internal/rpc"
)

const maxPanelTransfer = 16 << 20

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

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	var res rpc.TransferFileResult
	if err := s.Agent.Call(rpc.MethodTransferFile, rpc.TransferFileParams{Site: site, RelPath: rel}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	name := filepath.Base(res.Name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(res.Data)))
	w.Write(res.Data)
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	r.Body = http.MaxBytesReader(w, r.Body, maxPanelTransfer)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		respondErr(w, http.StatusRequestEntityTooLarge, "upload exceeds 16 MB; use SFTP for larger files")
		return
	}
	var res rpc.TransferFileResult
	if err := s.Agent.Call(rpc.MethodTransferFile, rpc.TransferFileParams{Site: site, RelPath: rel, Data: data, Upload: true}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "file.upload", site.Domain, rel)
	respond(w, http.StatusCreated, res)
}

func (s *Server) handleManageFile(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Dest      string `json:"dest,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	var out map[string]string
	if err := s.Agent.Call(rpc.MethodManageFile, rpc.ManageFileParams{Site: site, Operation: req.Operation, RelPath: req.Path, DestPath: req.Dest}, &out); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "file."+req.Operation, site.Domain, req.Path)
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
	if err := s.Agent.Call(rpc.MethodSetSFTP, rpc.SFTPParams{Site: site, Password: req.Password, Enable: req.Enable}, &out); err != nil {
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

func (s *Server) handleListSSHKeys(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var res rpc.SSHKeysResult
	if err := s.Agent.Call(rpc.MethodSSHKeys, rpc.SSHKeyParams{Site: site, Action: "list"}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	respond(w, http.StatusOK, res)
}

func (s *Server) handleAddSSHKey(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
	}
	if err := decode(r, &req); err != nil || req.PublicKey == "" {
		respondErr(w, http.StatusBadRequest, "public_key required")
		return
	}
	var res rpc.SSHKeysResult
	if err := s.Agent.Call(rpc.MethodSSHKeys, rpc.SSHKeyParams{Site: site, Action: "add", PublicKey: req.PublicKey}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	fingerprint := ""
	if len(res.Keys) > 0 {
		fingerprint = res.Keys[len(res.Keys)-1].Fingerprint
	}
	s.Store.Audit(s.actor(r), "sftp.key.add", site.Domain, fingerprint)
	respond(w, http.StatusCreated, res)
}

func (s *Server) handleDeleteSSHKey(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOr404(w, r)
	if !ok {
		return
	}
	fingerprint := r.PathValue("fingerprint")
	var res rpc.SSHKeysResult
	if err := s.Agent.Call(rpc.MethodSSHKeys, rpc.SSHKeyParams{Site: site, Action: "delete", Fingerprint: fingerprint}, &res); err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.Store.Audit(s.actor(r), "sftp.key.delete", site.Domain, fingerprint)
	respond(w, http.StatusOK, res)
}
