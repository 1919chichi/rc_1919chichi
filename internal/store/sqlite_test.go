package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestFetchPendingJobs_ReclaimsStaleProcessing(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateJob(model.CreateNotificationRequest{
		URL:    "https://example.com/hook",
		Method: "POST",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	first, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("expected 1 claimed job, got %d", len(first))
	}
	if first[0].ID != created.ID {
		t.Fatalf("unexpected job id: got %d want %d", first[0].ID, created.ID)
	}

	second, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("expected 0 claimed jobs, got %d", len(second))
	}

	staleAt := time.Now().Add(-2 * time.Minute).Unix()
	if _, err := s.db.Exec(
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		model.StatusProcessing, staleAt, created.ID,
	); err != nil {
		t.Fatalf("set stale processing: %v", err)
	}

	reclaimed, err := s.FetchPendingJobs(10, time.Minute)
	if err != nil {
		t.Fatalf("reclaim fetch: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("expected 1 reclaimed job, got %d", len(reclaimed))
	}
	if reclaimed[0].ID != created.ID {
		t.Fatalf("unexpected reclaimed job id: got %d want %d", reclaimed[0].ID, created.ID)
	}
}

func TestGetJob_InvalidHeaderJSONFallsBackToEmptyMap(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateJob(model.CreateNotificationRequest{
		URL:    "https://example.com/hook",
		Method: "POST",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if _, err := s.db.Exec(`UPDATE jobs SET headers = ? WHERE id = ?`, "{invalid json", created.ID); err != nil {
		t.Fatalf("corrupt headers: %v", err)
	}

	job, err := s.GetJob(created.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(job.Headers) != 0 {
		t.Fatalf("expected empty headers on invalid JSON, got: %#v", job.Headers)
	}
}
