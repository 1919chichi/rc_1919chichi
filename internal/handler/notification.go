package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
	"github.com/1919chichi/rc_1919chichi/internal/vendor"
)

type Handler struct {
	store    *store.Store
	registry *vendor.Registry
}

const maxRequestBodyBytes = 1 << 20 // 1 MB

func New(s *store.Store, r *vendor.Registry) *Handler {
	return &Handler{store: s, registry: r}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/notifications", h.handleNotifications)
	mux.HandleFunc("/api/notifications/", h.handleNotificationByID)
	mux.HandleFunc("/api/vendors", h.handleVendors)
	mux.HandleFunc("/api/vendors/", h.handleVendorByID)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func (h *Handler) handleNotifications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.Create(w, r)
	default:
		methodNotAllowed(w, http.MethodPost)
	}
}

func (h *Handler) handleNotificationByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/notifications/")
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return
	}

	if path == "failed" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.ListFailed(w, r)
		return
	}

	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 1:
		id, err := parseJobID(parts[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.Get(w, r, id)
	case len(parts) == 2 && parts[1] == "replay":
		id, err := parseJobID(parts[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.Replay(w, r, id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
	}
}

// Create handles POST /api/notifications.
// Callers submit {vendor_id, event, payload}; the system resolves the vendor
// adapter, builds the HTTP request, and enqueues a job.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req model.CreateNotificationRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body must contain a single JSON object"})
		return
	}

	req.VendorID = strings.TrimSpace(req.VendorID)
	req.Event = strings.TrimSpace(req.Event)

	if req.VendorID == "" || req.Event == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vendor_id and event are required"})
		return
	}

	adapter, err := h.registry.Resolve(req.VendorID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	resolved, err := adapter.BuildRequest(req.Event, req.Payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to build request: " + err.Error()})
		return
	}

	job, err := h.store.CreateJob(model.CreateJobParams{
		VendorID:   req.VendorID,
		Event:      req.Event,
		URL:        resolved.URL,
		Method:     resolved.Method,
		Headers:    resolved.Headers,
		Body:       resolved.Body,
		MaxRetries: resolved.MaxRetries,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to enqueue notification"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "notification enqueued",
		"job":     job,
	})
}

func (h *Handler) Get(w http.ResponseWriter, _ *http.Request, id int64) {
	job, err := h.store.GetJob(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) ListFailed(w http.ResponseWriter, _ *http.Request) {
	jobs, err := h.store.ListFailedJobs()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list jobs"})
		return
	}
	if jobs == nil {
		jobs = []model.Job{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "count": len(jobs)})
}

func (h *Handler) Replay(w http.ResponseWriter, _ *http.Request, id int64) {
	job, err := h.store.ResetJob(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "job replayed",
		"job":     job,
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func methodNotAllowed(w http.ResponseWriter, allowedMethods ...string) {
	if len(allowedMethods) > 0 {
		w.Header().Set("Allow", strings.Join(allowedMethods, ", "))
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func parseJobID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, errors.New("empty job id")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid job id")
	}
	return id, nil
}
