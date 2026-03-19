package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

func respondSuccess(w http.ResponseWriter, httpStatus int, data any) {
	writeResponse(w, httpStatus, model.Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

func respondList(w http.ResponseWriter, items any, total int) {
	writeResponse(w, http.StatusOK, model.Response{
		Code:    0,
		Message: "success",
		Data:    model.ListData{Items: items, Total: total},
	})
}

func respondError(w http.ResponseWriter, httpStatus int, msg string) {
	writeResponse(w, httpStatus, model.Response{
		Code:    httpStatus,
		Message: msg,
	})
}

func writeResponse(w http.ResponseWriter, status int, resp model.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func methodNotAllowed(w http.ResponseWriter, allowedMethods ...string) {
	if len(allowedMethods) > 0 {
		w.Header().Set("Allow", strings.Join(allowedMethods, ", "))
	}
	respondError(w, http.StatusMethodNotAllowed, "method not allowed")
}
