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

// TemplateData is the context available inside body templates.
type TemplateData struct {
	Event     string
	Payload   map[string]any
	Timestamp string
}

// ConfigAdapter builds HTTP requests from a database-stored VendorConfig
// using Go text/template for the body and rule-based auth injection.
type ConfigAdapter struct {
	config model.VendorConfig
}

func NewConfigAdapter(config model.VendorConfig) *ConfigAdapter {
	return &ConfigAdapter{config: config}
}

func (a *ConfigAdapter) VendorID() string {
	return a.config.ID
}

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

func (a *ConfigAdapter) renderBody(event string, payload map[string]any) (string, error) {
	if a.config.BodyTpl == "" {
		return "", nil
	}

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

func (a *ConfigAdapter) injectAuth(headers map[string]string) error {
	switch a.config.AuthType {
	case "bearer":
		token, ok := a.config.AuthConfig["token"]
		if !ok {
			return fmt.Errorf("bearer auth requires \"token\" in auth_config")
		}
		headers["Authorization"] = "Bearer " + token

	case "api_key":
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
