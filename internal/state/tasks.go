package state

import (
	"database/sql"
	"errors"
)

// CreateTask enqueues a background task.
func (s *Store) CreateTask(kind string, siteID int64) (Task, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO tasks (kind, site_id, status, created_at) VALUES (?, ?, ?, ?)`,
		kind, siteID, string(TaskPending), ts)
	if err != nil {
		return Task{}, err
	}
	id, _ := res.LastInsertId()
	return Task{ID: id, Kind: kind, SiteID: siteID, Status: TaskPending, CreatedAt: parseTime(ts)}, nil
}

// StartTask marks a task running.
func (s *Store) StartTask(id int64) error {
	_, err := s.db.Exec(`UPDATE tasks SET status=?, started_at=? WHERE id=?`, string(TaskRunning), now(), id)
	return err
}

// ProgressTask updates progress (0–100), a status message, and appends to the log.
func (s *Store) ProgressTask(id int64, progress int, message string) error {
	_, err := s.db.Exec(`UPDATE tasks SET progress=?, message=?, log = log || ? WHERE id=?`,
		progress, message, message+"\n", id)
	return err
}

// FinishTask records the terminal state of a task.
func (s *Store) FinishTask(id int64, taskErr error) error {
	status, errMsg, progress := string(TaskSucceeded), "", 100
	if taskErr != nil {
		status, errMsg = string(TaskFailed), taskErr.Error()
		var cur Task
		if t, err := s.GetTask(id); err == nil {
			cur = t
		}
		progress = cur.Progress
	}
	_, err := s.db.Exec(`UPDATE tasks SET status=?, error=?, progress=?, finished_at=? WHERE id=?`,
		status, errMsg, progress, now(), id)
	return err
}

const taskCols = `id, kind, site_id, status, progress, message, log, error, created_at, started_at, finished_at`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var status, created string
	var started, finished sql.NullString
	err := row.Scan(&t.ID, &t.Kind, &t.SiteID, &status, &t.Progress, &t.Message, &t.Log, &t.Error,
		&created, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	t.Status = TaskStatus(status)
	t.CreatedAt = parseTime(created)
	t.StartedAt = parseTimePtr(started)
	t.FinishedAt = parseTimePtr(finished)
	return t, nil
}

// GetTask fetches one task.
func (s *Store) GetTask(id int64) (Task, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
}

// ListTasks returns the most recent tasks, newest first.
func (s *Store) ListTasks(limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}
