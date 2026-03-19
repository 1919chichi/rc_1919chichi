package adapter

// ResolvedRequest holds the fully-resolved HTTP request parameters
// produced by a VendorAdapter.
type ResolvedRequest struct {
	URL        string
	Method     string
	Headers    map[string]string
	Body       string
	MaxRetries int
}

// VendorAdapter transforms a business event into a concrete HTTP request
// targeting a specific vendor's API.
type VendorAdapter interface {
	VendorID() string
	BuildRequest(event string, payload map[string]any) (*ResolvedRequest, error)
}
