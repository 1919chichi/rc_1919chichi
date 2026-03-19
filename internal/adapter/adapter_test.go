package adapter

import (
	"strings"
	"testing"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

// --- ConfigAdapter tests ---

func TestConfigAdapter_RenderBodyTemplate(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:      "test",
		BaseURL: "https://api.example.com/events",
		Method:  "POST",
		BodyTpl: `{"event": {{json .Event}}, "user_id": {{json .Payload.user_id}}}`,
	})

	resolved, err := adapter.BuildRequest("user_registered", map[string]any{
		"user_id": 42,
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if resolved.URL != "https://api.example.com/events" {
		t.Fatalf("unexpected url: %q", resolved.URL)
	}
	if resolved.Method != "POST" {
		t.Fatalf("unexpected method: %q", resolved.Method)
	}
	if !strings.Contains(resolved.Body, `"user_registered"`) {
		t.Fatalf("body missing event: %s", resolved.Body)
	}
	if !strings.Contains(resolved.Body, "42") {
		t.Fatalf("body missing user_id: %s", resolved.Body)
	}
}

func TestConfigAdapter_BearerAuth(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:         "test",
		BaseURL:    "https://api.example.com",
		Method:     "POST",
		AuthType:   "bearer",
		AuthConfig: map[string]string{"token": "my-secret-token"},
	})

	resolved, err := adapter.BuildRequest("test_event", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if resolved.Headers["Authorization"] != "Bearer my-secret-token" {
		t.Fatalf("expected bearer auth header, got: %v", resolved.Headers)
	}
}

func TestConfigAdapter_ApiKeyAuth(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:         "test",
		BaseURL:    "https://api.example.com",
		Method:     "POST",
		AuthType:   "api_key",
		AuthConfig: map[string]string{"header": "X-Api-Key", "key": "abc123"},
	})

	resolved, err := adapter.BuildRequest("test_event", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if resolved.Headers["X-Api-Key"] != "abc123" {
		t.Fatalf("expected api key header, got: %v", resolved.Headers)
	}
}

func TestConfigAdapter_BasicAuth(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:         "test",
		BaseURL:    "https://api.example.com",
		Method:     "POST",
		AuthType:   "basic",
		AuthConfig: map[string]string{"username": "user", "password": "pass"},
	})

	resolved, err := adapter.BuildRequest("test_event", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	auth := resolved.Headers["Authorization"]
	if !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("expected basic auth header, got: %q", auth)
	}
}

func TestConfigAdapter_MergesDefaultHeaders(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:      "test",
		BaseURL: "https://api.example.com",
		Method:  "POST",
		Headers: map[string]string{
			"Content-Type": "application/json",
			"X-Custom":     "value",
		},
	})

	resolved, err := adapter.BuildRequest("test_event", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if resolved.Headers["Content-Type"] != "application/json" {
		t.Fatalf("missing Content-Type header")
	}
	if resolved.Headers["X-Custom"] != "value" {
		t.Fatalf("missing X-Custom header")
	}
}

func TestConfigAdapter_EmptyBodyTemplate(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:      "test",
		BaseURL: "https://api.example.com",
		Method:  "GET",
	})

	resolved, err := adapter.BuildRequest("ping", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if resolved.Body != "" {
		t.Fatalf("expected empty body, got: %q", resolved.Body)
	}
}

func TestConfigAdapter_UnsupportedAuthType(t *testing.T) {
	adapter := NewConfigAdapter(model.VendorConfig{
		ID:       "test",
		BaseURL:  "https://api.example.com",
		Method:   "POST",
		AuthType: "oauth2",
	})

	_, err := adapter.BuildRequest("test_event", nil)
	if err == nil {
		t.Fatal("expected error for unsupported auth type")
	}
}

// --- Registry tests ---

type stubAdapter struct {
	id string
}

func (a *stubAdapter) VendorID() string { return a.id }
func (a *stubAdapter) BuildRequest(event string, payload map[string]any) (*ResolvedRequest, error) {
	return &ResolvedRequest{
		URL:    "https://code-adapter.example.com",
		Method: "POST",
	}, nil
}

type stubVendorStore struct {
	vendors map[string]*model.VendorConfig
}

func (s *stubVendorStore) GetVendor(id string) (*model.VendorConfig, error) {
	v, ok := s.vendors[id]
	if !ok {
		return nil, &vendorNotFoundError{id: id}
	}
	return v, nil
}

type vendorNotFoundError struct{ id string }

func (e *vendorNotFoundError) Error() string { return "not found: " + e.id }

func TestRegistry_CodeAdapterTakesPriority(t *testing.T) {
	fakeStore := &stubVendorStore{
		vendors: map[string]*model.VendorConfig{
			"my_vendor": {
				ID:       "my_vendor",
				BaseURL:  "https://config-adapter.example.com",
				Method:   "PUT",
				IsActive: true,
			},
		},
	}

	reg := NewRegistry(fakeStore)
	reg.Register(&stubAdapter{id: "my_vendor"})

	adapter, err := reg.Resolve("my_vendor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	resolved, err := adapter.BuildRequest("test", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	// Code adapter should win over config adapter
	if resolved.URL != "https://code-adapter.example.com" {
		t.Fatalf("expected code adapter URL, got %q", resolved.URL)
	}
}

func TestRegistry_FallsBackToConfigAdapter(t *testing.T) {
	fakeStore := &stubVendorStore{
		vendors: map[string]*model.VendorConfig{
			"db_vendor": {
				ID:       "db_vendor",
				BaseURL:  "https://config-adapter.example.com",
				Method:   "POST",
				IsActive: true,
			},
		},
	}

	reg := NewRegistry(fakeStore)

	adapter, err := reg.Resolve("db_vendor")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	resolved, err := adapter.BuildRequest("test", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	if resolved.URL != "https://config-adapter.example.com" {
		t.Fatalf("expected config adapter URL, got %q", resolved.URL)
	}
}

func TestRegistry_ReturnsErrorForUnknownVendor(t *testing.T) {
	fakeStore := &stubVendorStore{vendors: map[string]*model.VendorConfig{}}
	reg := NewRegistry(fakeStore)

	_, err := reg.Resolve("unknown")
	if err == nil {
		t.Fatal("expected error for unknown vendor")
	}
}

func TestRegistry_RejectsInactiveVendor(t *testing.T) {
	fakeStore := &stubVendorStore{
		vendors: map[string]*model.VendorConfig{
			"inactive": {
				ID:       "inactive",
				BaseURL:  "https://example.com",
				Method:   "POST",
				IsActive: false,
			},
		},
	}

	reg := NewRegistry(fakeStore)
	_, err := reg.Resolve("inactive")
	if err == nil {
		t.Fatal("expected error for inactive vendor")
	}
}
