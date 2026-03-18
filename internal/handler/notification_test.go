package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/1919chichi/rc_1919chichi/internal/store"
)

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	h := New(s)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestHandleNotificationByID_StrictPath(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/notifications/1/extra", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreate_NormalizesMethod(t *testing.T) {
	mux := newTestMux(t)

	body := []byte(`{"url":"https://example.com/hook","method":"post"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Job struct {
			Method string `json:"method"`
		} `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Job.Method != http.MethodPost {
		t.Fatalf("expected method %q, got %q", http.MethodPost, resp.Job.Method)
	}
}

func TestCreate_RejectsUnknownFields(t *testing.T) {
	mux := newTestMux(t)

	body := []byte(`{"url":"https://example.com/hook","method":"POST","unexpected":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
