package model

import "time"

type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

const DefaultMaxRetries = 3

type Job struct {
	ID          int64             `json:"id"`
	URL         string            `json:"url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	Status      JobStatus         `json:"status"`
	RetryCount  int               `json:"retry_count"`
	MaxRetries  int               `json:"max_retries"`
	NextRetryAt time.Time         `json:"next_retry_at"`
	LastError   string            `json:"last_error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// CreateNotificationRequest is the payload accepted by POST /api/notifications.
type CreateNotificationRequest struct {
	URL     string            `json:"url" binding:"required"`
	Method  string            `json:"method" binding:"required"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// BackoffDuration returns the delay before the next retry using exponential backoff.
// retry 0 -> 10s, retry 1 -> 30s, retry 2 -> 90s, ...
func BackoffDuration(retryCount int) time.Duration {
	base := 10 * time.Second
	for i := 0; i < retryCount; i++ {
		base *= 3
	}
	return base
}
