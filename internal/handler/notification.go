package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
)

type Handler struct {
	store *store.Store
}

func New(s *store.Store) *Handler {
	return &Handler{store: s}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/notifications", h.handleNotifications)
	mux.HandleFunc("/api/notifications/", h.handleNotificationByID)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func (h *Handler) handleNotifications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.Create(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleNotificationByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/notifications/")

	if path == "failed" && r.Method == http.MethodGet {
		h.ListFailed(w, r)
		return
	}

	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
		return
	}

	if len(parts) == 2 && parts[1] == "replay" && r.Method == http.MethodPost {
		h.Replay(w, r, id)
		return
	}

	if r.Method == http.MethodGet {
		h.Get(w, r, id)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}

	if req.URL == "" || req.Method == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url and method are required"})
		return
	}

	allowed := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	if !allowed[req.Method] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "method must be one of GET, POST, PUT, PATCH, DELETE"})
		return
	}

	job, err := h.store.CreateJob(req)
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
	json.NewEncoder(w).Encode(data)
}
