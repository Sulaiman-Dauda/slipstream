package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// runTask records a background task and executes fn in a goroutine. fn
// receives a progress callback; panics are converted into task failures so
// one bad operation can never take the API down.
func (s *Server) runTask(kind string, siteID int64, fn func(progress func(int, string)) error) (state.Task, error) {
	task, err := s.Store.CreateTask(kind, siteID)
	if err != nil {
		return state.Task{}, err
	}
	go func() {
		s.Store.StartTask(task.ID)
		var runErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					runErr = fmt.Errorf("internal panic: %v", r)
					s.Log.Error("task panicked", "task", task.ID, "kind", kind, "panic", r)
				}
			}()
			runErr = fn(func(pct int, msg string) {
				s.Store.ProgressTask(task.ID, pct, msg)
			})
		}()
		if err := s.Store.FinishTask(task.ID, runErr); err != nil {
			s.Log.Error("finish task", "task", task.ID, "err", err)
		}
	}()
	return task, nil
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Store.ListTasks(100)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.Store.GetTask(id)
	if errors.Is(err, state.ErrNotFound) {
		respondErr(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, task)
}

// handleEvents is the SSE stream driving live progress in the UI. It sends
// the running/recent task set every 2 seconds until the client goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	send := func() bool {
		tasks, err := s.Store.ListTasks(20)
		if err != nil {
			return false
		}
		payload, _ := json.Marshal(tasks)
		if _, err := fmt.Fprintf(w, "event: tasks\ndata: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}
