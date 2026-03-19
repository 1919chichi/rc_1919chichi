// package model —— 供应商（Vendor）相关的数据模型
package model

import "time"

// VendorConfig 表示一个第三方回调供应商的配置
// 存储了请求的 base_url、认证方式、请求模板等信息
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

// CreateVendorRequest 创建供应商时的请求体
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

// UpdateVendorRequest 更新供应商时的请求体（不含 id，通过 URL 路径指定）
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

// CreateJobParams 持久化任务时的参数
// 包含已解析好的 HTTP 请求详情（URL、Method、Headers、Body 等）
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
