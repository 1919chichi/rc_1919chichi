package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS jobs (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		url           TEXT    NOT NULL,
		method        TEXT    NOT NULL DEFAULT 'POST',
		headers       TEXT    NOT NULL DEFAULT '{}',
		body          TEXT    NOT NULL DEFAULT '',
		status        TEXT    NOT NULL DEFAULT 'pending',
		retry_count   INTEGER NOT NULL DEFAULT 0,
		max_retries   INTEGER NOT NULL DEFAULT 3,
		next_retry_at INTEGER NOT NULL DEFAULT 0,
		last_error    TEXT    NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL DEFAULT 0,
		updated_at    INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_jobs_status_next_retry ON jobs(status, next_retry_at);
	`
	_, err := s.db.Exec(query)
	return err
}

func (s *Store) CreateJob(req model.CreateNotificationRequest) (*model.Job, error) {
	headersJSON, err := json.Marshal(req.Headers)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}

	now := time.Now().Unix()
	result, err := s.db.Exec(
		`INSERT INTO jobs (url, method, headers, body, status, max_retries, next_retry_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Method, string(headersJSON), req.Body,
		model.StatusPending, model.DefaultMaxRetries, now, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	id, _ := result.LastInsertId()
	return s.GetJob(id)
}

// FetchPendingJobs atomically selects and locks up to `limit` pending jobs
// whose next_retry_at has passed.
func (s *Store) FetchPendingJobs(limit int) ([]model.Job, error) {
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id FROM jobs
		 WHERE status = ? AND next_retry_at <= ?
		 ORDER BY next_retry_at ASC
		 LIMIT ?`,
		model.StatusPending, now, limit,
	)
	if err != nil {
		return nil, err
	}

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	for _, id := range ids {
		_, err := tx.Exec(
			`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
			model.StatusProcessing, now, id,
		)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	jobs := make([]model.Job, 0, len(ids))
	for _, id := range ids {
		job, err := s.GetJob(id)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, nil
}

func (s *Store) GetJob(id int64) (*model.Job, error) {
	row := s.db.QueryRow(
		`SELECT id, url, method, headers, body, status, retry_count, max_retries,
		        next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

func (s *Store) MarkCompleted(id int64) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		model.StatusCompleted, now, id,
	)
	return err
}

func (s *Store) MarkRetry(id int64, nextRetryAt time.Time) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, retry_count = retry_count + 1, next_retry_at = ?, updated_at = ? WHERE id = ?`,
		model.StatusPending, nextRetryAt.Unix(), now, id,
	)
	return err
}

func (s *Store) MarkFailed(id int64, lastError string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		model.StatusFailed, lastError, now, id,
	)
	return err
}

func (s *Store) ListFailedJobs() ([]model.Job, error) {
	rows, err := s.db.Query(
		`SELECT id, url, method, headers, body, status, retry_count, max_retries,
		        next_retry_at, last_error, created_at, updated_at
		 FROM jobs WHERE status = ? ORDER BY updated_at DESC`, model.StatusFailed,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []model.Job
	for rows.Next() {
		job, err := scanJobFromRows(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, nil
}

// ResetJob replays a failed job by resetting it to pending.
func (s *Store) ResetJob(id int64) (*model.Job, error) {
	now := time.Now().Unix()
	result, err := s.db.Exec(
		`UPDATE jobs SET status = ?, retry_count = 0, next_retry_at = ?, last_error = '', updated_at = ? WHERE id = ? AND status = ?`,
		model.StatusPending, now, now, id, model.StatusFailed,
	)
	if err != nil {
		return nil, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return nil, fmt.Errorf("job %d not found or not in failed status", id)
	}
	return s.GetJob(id)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanJob(row scannable) (*model.Job, error) {
	var j model.Job
	var headersStr, status string
	var nextRetry, created, updated int64

	err := row.Scan(&j.ID, &j.URL, &j.Method, &headersStr, &j.Body, &status,
		&j.RetryCount, &j.MaxRetries, &nextRetry, &j.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}

	j.Status = model.JobStatus(status)
	json.Unmarshal([]byte(headersStr), &j.Headers)
	j.NextRetryAt = time.Unix(nextRetry, 0).UTC()
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return &j, nil
}

func scanJobFromRows(rows *sql.Rows) (*model.Job, error) {
	var j model.Job
	var headersStr, status string
	var nextRetry, created, updated int64

	err := rows.Scan(&j.ID, &j.URL, &j.Method, &headersStr, &j.Body, &status,
		&j.RetryCount, &j.MaxRetries, &nextRetry, &j.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}

	j.Status = model.JobStatus(status)
	json.Unmarshal([]byte(headersStr), &j.Headers)
	j.NextRetryAt = time.Unix(nextRetry, 0).UTC()
	j.CreatedAt = time.Unix(created, 0).UTC()
	j.UpdatedAt = time.Unix(updated, 0).UTC()
	return &j, nil
}
