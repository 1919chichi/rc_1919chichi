// package adapter 的 ConfigAdapter 实现：从数据库中的 VendorConfig 动态构建 HTTP 请求
package adapter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"text/template"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

// TemplateData 是 body 模板中可用的上下文数据
// 模板中可通过 {{.Event}}、{{.Payload}}、{{.Timestamp}} 等访问
type TemplateData struct {
	Event     string         // 事件名
	Payload   map[string]any // 事件负载（业务数据）
	Timestamp string         // 当前时间（RFC3339 格式）
}

// ConfigAdapter 根据数据库中的 VendorConfig 构建 HTTP 请求
// 使用 Go text/template 渲染请求体，并按规则注入认证信息（bearer、api_key、basic）
type ConfigAdapter struct {
	config model.VendorConfig // 厂商配置（从 vendors 表加载）
}

// NewConfigAdapter 创建 ConfigAdapter 实例
func NewConfigAdapter(config model.VendorConfig) *ConfigAdapter {
	return &ConfigAdapter{config: config}
}

// VendorID 返回该适配器对应的厂商 ID
func (a *ConfigAdapter) VendorID() string {
	return a.config.ID
}

// BuildRequest 根据事件和负载构建完整的 HTTP 请求
// 步骤：1) 用模板渲染 body 2) 合并 headers 3) 注入认证
func (a *ConfigAdapter) BuildRequest(event string, payload map[string]any) (*ResolvedRequest, error) {
	body, err := a.renderBody(event, payload)
	if err != nil {
		return nil, fmt.Errorf("render body template: %w", err)
	}

	headers := make(map[string]string)
	for k, v := range a.config.Headers {
		headers[k] = v
	}

	if err := a.injectAuth(headers); err != nil {
		return nil, fmt.Errorf("inject auth: %w", err)
	}

	return &ResolvedRequest{
		URL:        a.config.BaseURL,
		Method:     a.config.Method,
		Headers:    headers,
		Body:       body,
		MaxRetries: a.config.MaxRetries,
	}, nil
}

// renderBody 用 Go text/template 渲染请求体
// 模板中可用 {{.Event}}、{{.Payload}}、{{.Timestamp}}，以及 {{json .Payload}} 等函数
func (a *ConfigAdapter) renderBody(event string, payload map[string]any) (string, error) {
	if a.config.BodyTpl == "" {
		return "", nil
	}

	// 注册自定义函数，模板中可用 {{json .Payload}} 输出 JSON
	funcMap := template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}

	tmpl, err := template.New("body").Funcs(funcMap).Parse(a.config.BodyTpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	data := TemplateData{
		Event:     event,
		Payload:   payload,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// injectAuth 根据 auth_type 向 headers 注入认证信息
// 支持：bearer（Authorization: Bearer xxx）、api_key（自定义 header）、basic（用户名密码 Base64）
// injectAuth 根据 auth_type 将认证信息注入到 headers 中
// 支持 bearer、api_key、basic 三种内置认证方式
func (a *ConfigAdapter) injectAuth(headers map[string]string) error {
	switch a.config.AuthType {
	case "bearer":
		// Bearer Token：在 Authorization 头中添加 "Bearer <token>"
		token, ok := a.config.AuthConfig["token"]
		if !ok {
			return fmt.Errorf("bearer auth requires \"token\" in auth_config")
		}
		headers["Authorization"] = "Bearer " + token

	case "api_key":
		// API Key：可指定自定义 header 名和 key 值，常用于 X-API-Key 等
		h, ok := a.config.AuthConfig["header"]
		if !ok {
			return fmt.Errorf("api_key auth requires \"header\" in auth_config")
		}
		key, ok := a.config.AuthConfig["key"]
		if !ok {
			return fmt.Errorf("api_key auth requires \"key\" in auth_config")
		}
		headers[h] = key

	case "basic":
		// Basic Auth：将 username:password 做 Base64 编码后放入 Authorization 头
		username := a.config.AuthConfig["username"]
		password := a.config.AuthConfig["password"]
		cred := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		headers["Authorization"] = "Basic " + cred

	case "":
		// no auth

	default:
		return fmt.Errorf("unsupported auth_type %q (use a code adapter for custom auth)", a.config.AuthType)
	}
	return nil
}
