package model

// Response is the unified API response envelope used by all endpoints.
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ListData wraps paginated/collection results inside Response.Data.
type ListData struct {
	Items any `json:"items"`
	Total int `json:"total"`
}
