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
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFetchPendingJobs_ReclaimsStaleProcessing(t *testing.T) {
	s := newTestStore(t)

	created, _, err := s.CreateJob(model.CreateJobParams{
		VendorID: "test_vendor",
		Event:    "user_registered",
		BizID:    "user_001",
		URL:      "https://example.com/hook",
		Method:   "POST",
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

	created, _, err := s.CreateJob(model.CreateJobParams{
		VendorID: "test_vendor",
		Event:    "test_event",
		BizID:    "biz_001",
		URL:      "https://example.com/hook",
		Method:   "POST",
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

func TestCreateJob_StoresVendorIDAndEvent(t *testing.T) {
	s := newTestStore(t)

	job, isNew, err := s.CreateJob(model.CreateJobParams{
		VendorID:   "ad_system",
		Event:      "conversion",
		BizID:      "click_12345",
		URL:        "https://ads.example.com/track",
		Method:     "POST",
		Headers:    map[string]string{"X-Api-Key": "abc"},
		Body:       `{"event":"conversion"}`,
		MaxRetries: 5,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !isNew {
		t.Fatal("expected isNew to be true for first insert")
	}

	if job.VendorID != "ad_system" {
		t.Fatalf("expected vendor_id %q, got %q", "ad_system", job.VendorID)
	}
	if job.Event != "conversion" {
		t.Fatalf("expected event %q, got %q", "conversion", job.Event)
	}
	if job.BizID != "click_12345" {
		t.Fatalf("expected biz_id %q, got %q", "click_12345", job.BizID)
	}
	if job.MaxRetries != 5 {
		t.Fatalf("expected max_retries 5, got %d", job.MaxRetries)
	}
}

func TestCreateJob_Idempotent(t *testing.T) {
	s := newTestStore(t)

	params := model.CreateJobParams{
		VendorID: "ad_system",
		Event:    "user_registered",
		BizID:    "user_10086",
		URL:      "https://ads.example.com/callback",
		Method:   "POST",
		Body:     `{"user_id":10086}`,
	}

	first, isNew, err := s.CreateJob(params)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !isNew {
		t.Fatal("expected first insert to be new")
	}

	second, isNew, err := s.CreateJob(params)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if isNew {
		t.Fatal("expected second insert to be duplicate")
	}
	if second.ID != first.ID {
		t.Fatalf("expected same job id %d, got %d", first.ID, second.ID)
	}
}

func TestSeedDefaultVendors(t *testing.T) {
	s := newTestStore(t)

	if err := s.SeedDefaultVendors(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	vendors, err := s.ListVendors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(vendors) != 3 {
		t.Fatalf("expected 3 seeded vendors, got %d", len(vendors))
	}

	// calling again should not duplicate
	if err := s.SeedDefaultVendors(); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	vendors, err = s.ListVendors()
	if err != nil {
		t.Fatalf("list after re-seed: %v", err)
	}
	if len(vendors) != 3 {
		t.Fatalf("expected 3 vendors after re-seed, got %d", len(vendors))
	}
}

func TestVendorCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create
	v, err := s.CreateVendor(model.CreateVendorRequest{
		ID:         "crm",
		Name:       "CRM System",
		BaseURL:    "https://crm.example.com/api",
		Method:     "POST",
		AuthType:   "bearer",
		AuthConfig: map[string]string{"token": "secret"},
		Headers:    map[string]string{"Content-Type": "application/json"},
		BodyTpl:    `{"event":"{{.Event}}"}`,
		MaxRetries: 5,
	})
	if err != nil {
		t.Fatalf("create vendor: %v", err)
	}
	if v.ID != "crm" || v.Name != "CRM System" || !v.IsActive {
		t.Fatalf("unexpected vendor: %+v", v)
	}

	// Get
	got, err := s.GetVendor("crm")
	if err != nil {
		t.Fatalf("get vendor: %v", err)
	}
	if got.AuthType != "bearer" || got.AuthConfig["token"] != "secret" {
		t.Fatalf("unexpected auth config: %+v", got)
	}

	// List
	list, err := s.ListVendors()
	if err != nil {
		t.Fatalf("list vendors: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 vendor, got %d", len(list))
	}

	// Update
	updated, err := s.UpdateVendor("crm", model.UpdateVendorRequest{
		Name:    "CRM Updated",
		BaseURL: "https://crm2.example.com/api",
		Method:  "PUT",
	})
	if err != nil {
		t.Fatalf("update vendor: %v", err)
	}
	if updated.Name != "CRM Updated" || updated.Method != "PUT" {
		t.Fatalf("unexpected update result: %+v", updated)
	}

	// Delete (soft)
	if err := s.DeleteVendor("crm"); err != nil {
		t.Fatalf("delete vendor: %v", err)
	}
	got, err = s.GetVendor("crm")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got.IsActive {
		t.Fatal("expected vendor to be inactive after delete")
	}
}
