package model

import "time"

type VendorConfig struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	BaseURL    string            `json:"base_url"`
	Method     string            `json:"method"`
	AuthType   string            `json:"auth_type"`
	AuthConfig map[string]string `json:"auth_config,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	BodyTpl    string            `json:"body_tpl,omitempty"`
	MaxRetries int               `json:"max_retries"`
	IsActive   bool              `json:"is_active"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type CreateVendorRequest struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	BaseURL    string            `json:"base_url"`
	Method     string            `json:"method"`
	AuthType   string            `json:"auth_type"`
	AuthConfig map[string]string `json:"auth_config"`
	Headers    map[string]string `json:"headers"`
	BodyTpl    string            `json:"body_tpl"`
	MaxRetries int               `json:"max_retries"`
}

type UpdateVendorRequest struct {
	Name       string            `json:"name"`
	BaseURL    string            `json:"base_url"`
	Method     string            `json:"method"`
	AuthType   string            `json:"auth_type"`
	AuthConfig map[string]string `json:"auth_config"`
	Headers    map[string]string `json:"headers"`
	BodyTpl    string            `json:"body_tpl"`
	MaxRetries int               `json:"max_retries"`
}

// CreateJobParams contains the resolved HTTP request details for persisting a job.
type CreateJobParams struct {
	VendorID   string
	Event      string
	BizID      string
	URL        string
	Method     string
	Headers    map[string]string
	Body       string
	MaxRetries int
}
