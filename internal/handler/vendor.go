package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

func (h *Handler) handleVendors(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.CreateVendor(w, r)
	case http.MethodGet:
		h.ListVendors(w, r)
	default:
		methodNotAllowed(w, http.MethodPost, http.MethodGet)
	}
}

func (h *Handler) handleVendorByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/vendors/")
	if id == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return
	}
	// reject paths with extra segments like /api/vendors/foo/bar
	if strings.Contains(id, "/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "route not found"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.GetVendor(w, r, id)
	case http.MethodPut:
		h.UpdateVendor(w, r, id)
	case http.MethodDelete:
		h.DeleteVendor(w, r, id)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

func (h *Handler) CreateVendor(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req model.CreateVendorRequest
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

	req.ID = strings.TrimSpace(req.ID)
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)

	if req.ID == "" || req.Name == "" || req.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id, name and base_url are required"})
		return
	}

	if u, err := url.ParseRequestURI(req.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "base_url must be a valid http/https URL"})
		return
	}

	v, err := h.store.CreateVendor(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create vendor: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *Handler) GetVendor(w http.ResponseWriter, _ *http.Request, id string) {
	v, err := h.store.GetVendor(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "vendor not found"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) ListVendors(w http.ResponseWriter, _ *http.Request) {
	vendors, err := h.store.ListVendors()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list vendors"})
		return
	}
	if vendors == nil {
		vendors = []model.VendorConfig{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"vendors": vendors, "count": len(vendors)})
}

func (h *Handler) UpdateVendor(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req model.UpdateVendorRequest
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

	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)

	if req.Name == "" || req.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and base_url are required"})
		return
	}

	if u, err := url.ParseRequestURI(req.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "base_url must be a valid http/https URL"})
		return
	}

	v, err := h.store.UpdateVendor(id, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) DeleteVendor(w http.ResponseWriter, _ *http.Request, id string) {
	if err := h.store.DeleteVendor(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "vendor deactivated"})
}
