package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
	"github.com/1919chichi/rc_1919chichi/internal/adapter"
)

func newTestHandler(t *testing.T) (*http.ServeMux, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := adapter.NewRegistry(s)
	h := New(s, r)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, s
}

func seedVendor(t *testing.T, s *store.Store) {
	t.Helper()
	_, err := s.CreateVendor(model.CreateVendorRequest{
		ID:      "test_vendor",
		Name:    "Test Vendor",
		BaseURL: "https://example.com/hook",
		Method:  "POST",
		Headers: map[string]string{"Content-Type": "application/json"},
		BodyTpl: `{"event": {{json .Event}}, "data": {{json .Payload}}}`,
	})
	if err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
}

// decodeResponse is a test helper that unmarshals the unified response envelope.
func decodeResponse(t *testing.T, body []byte, data any) model.Response {
	t.Helper()
	var resp struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if data != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, data); err != nil {
			t.Fatalf("decode response data: %v", err)
		}
	}
	return model.Response{Code: resp.Code, Message: resp.Message}
}

func TestHandleNotificationByID_StrictPath(t *testing.T) {
	mux, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/notifications/1/extra", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreate_RequiresVendorIDEventAndBizID(t *testing.T) {
	mux, _ := newTestHandler(t)
	body := []byte(`{"vendor_id":"","event":"","biz_id":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreate_ResolvesVendorAndCreatesJob(t *testing.T) {
	mux, s := newTestHandler(t)
	seedVendor(t, s)

	body := []byte(`{"vendor_id":"test_vendor","event":"user_registered","biz_id":"user_123","payload":{"user_id":123}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	var job model.Job
	resp := decodeResponse(t, rec.Body.Bytes(), &job)
	if resp.Code != 0 {
		t.Fatalf("expected code 0, got %d", resp.Code)
	}
	if job.VendorID != "test_vendor" {
		t.Fatalf("expected vendor_id %q, got %q", "test_vendor", job.VendorID)
	}
	if job.Event != "user_registered" {
		t.Fatalf("expected event %q, got %q", "user_registered", job.Event)
	}
	if job.BizID != "user_123" {
		t.Fatalf("expected biz_id %q, got %q", "user_123", job.BizID)
	}
	if job.URL != "https://example.com/hook" {
		t.Fatalf("expected url from vendor config, got %q", job.URL)
	}
	if job.Method != "POST" {
		t.Fatalf("expected method POST, got %q", job.Method)
	}
}

func TestCreate_IdempotentDuplicate(t *testing.T) {
	mux, s := newTestHandler(t)
	seedVendor(t, s)

	body := []byte(`{"vendor_id":"test_vendor","event":"user_registered","biz_id":"user_456","payload":{"user_id":456}}`)

	// First request → 202
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Second request with same biz_id → 200
	req = httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreate_RejectsUnknownVendor(t *testing.T) {
	mux, _ := newTestHandler(t)
	body := []byte(`{"vendor_id":"nonexistent","event":"test","biz_id":"biz_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreate_RejectsUnknownFields(t *testing.T) {
	mux, _ := newTestHandler(t)
	body := []byte(`{"vendor_id":"test","event":"test","unexpected":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVendorCRUD(t *testing.T) {
	mux, _ := newTestHandler(t)

	// Create
	createBody := []byte(`{
		"id":"crm_vendor","name":"CRM","base_url":"https://crm.example.com/api",
		"method":"POST","auth_type":"bearer","auth_config":{"token":"secret"},
		"headers":{"Content-Type":"application/json"},
		"body_tpl":"{\"event\": \"{{.Event}}\"}","max_retries":5
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/vendors", bytes.NewReader(createBody))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create vendor: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Get
	req = httptest.NewRequest(http.MethodGet, "/api/vendors/crm_vendor", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get vendor: expected 200, got %d", rec.Code)
	}
	var v model.VendorConfig
	decodeResponse(t, rec.Body.Bytes(), &v)
	if v.Name != "CRM" || v.MaxRetries != 5 {
		t.Fatalf("unexpected vendor: %+v", v)
	}

	// List
	req = httptest.NewRequest(http.MethodGet, "/api/vendors", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list vendors: expected 200, got %d", rec.Code)
	}

	// Update
	updateBody := []byte(`{"name":"CRM Updated","base_url":"https://crm2.example.com/api","method":"PUT"}`)
	req = httptest.NewRequest(http.MethodPut, "/api/vendors/crm_vendor", bytes.NewReader(updateBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update vendor: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Delete (soft)
	req = httptest.NewRequest(http.MethodDelete, "/api/vendors/crm_vendor", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete vendor: expected 200, got %d", rec.Code)
	}
}
