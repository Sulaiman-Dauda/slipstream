package rpc

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"strconv"
	"sync"
)

// HandlerFunc handles one method. Params are the raw JSON parameters; the
// returned value is serialized into Response.Result.
type HandlerFunc func(params json.RawMessage) (any, error)

// Server dispatches authenticated requests to registered handlers.
type Server struct {
	token    string
	handlers map[string]HandlerFunc
	mu       sync.RWMutex
	log      *slog.Logger

	// SocketGroup, when set, becomes the group owner of the listening
	// socket so the unprivileged panel-api user can connect.
	SocketGroup string
}

// NewServer creates a Server that requires the given shared secret on every
// connection.
func NewServer(token string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{token: token, handlers: map[string]HandlerFunc{}, log: logger}
}

// Handle registers a handler for a method name.
func (s *Server) Handle(method string, fn HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = fn
}

// ListenAndServe binds the Unix socket at path (replacing any stale socket),
// restricts its mode, and serves until the listener fails.
func (s *Server) ListenAndServe(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %s: %w", path, err)
	}
	// Only owner and group may connect; the installer puts panel-api's user
	// in the agent's group.
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	if s.SocketGroup != "" {
		if grp, err := user.LookupGroup(s.SocketGroup); err == nil {
			if gid, err := strconv.Atoi(grp.Gid); err == nil {
				if err := os.Chown(path, -1, gid); err != nil {
					s.log.Warn("rpc: cannot chgrp socket", "group", s.SocketGroup, "err", err)
				}
			}
		} else {
			s.log.Warn("rpc: socket group not found", "group", s.SocketGroup)
		}
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 1<<20)
	enc := json.NewEncoder(conn)

	// Handshake first: one line containing the shared secret.
	var hs handshake
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	if err := json.Unmarshal(line, &hs); err != nil ||
		subtle.ConstantTimeCompare([]byte(hs.Auth), []byte(s.token)) != 1 {
		s.log.Warn("rpc: rejected connection with bad handshake")
		enc.Encode(Response{OK: false, Error: "unauthorized"})
		return
	}
	enc.Encode(Response{OK: true})

	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(Response{OK: false, Error: "malformed request"})
			return
		}
		resp := s.dispatch(req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(req Request) Response {
	s.mu.RLock()
	fn, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		return Response{ID: req.ID, OK: false, Error: fmt.Sprintf("unknown method %q", req.Method)}
	}
	result, err := fn(req.Params)
	if err != nil {
		s.log.Error("rpc: handler failed", "method", req.Method, "err", err)
		return Response{ID: req.ID, OK: false, Error: err.Error()}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Response{ID: req.ID, OK: false, Error: "marshal result: " + err.Error()}
	}
	return Response{ID: req.ID, OK: true, Result: raw}
}
