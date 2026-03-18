package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/model"
	"github.com/1919chichi/rc_1919chichi/internal/store"
)

type Dispatcher struct {
	store    *store.Store
	client   *http.Client
	interval time.Duration
	batch    int
}

func New(s *store.Store) *Dispatcher {
	return &Dispatcher{
		store: s,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		interval: 2 * time.Second,
		batch:    10,
	}
}

// Start begins the polling loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	log.Println("[worker] dispatcher started")
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[worker] dispatcher stopped")
			return
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

func (d *Dispatcher) poll(ctx context.Context) {
	jobs, err := d.store.FetchPendingJobs(d.batch)
	if err != nil {
		log.Printf("[worker] fetch jobs error: %v", err)
		return
	}
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
			d.deliver(job)
		}
	}
}

func (d *Dispatcher) deliver(job model.Job) {
	log.Printf("[worker] delivering job %d -> %s %s (retry %d/%d)",
		job.ID, job.Method, job.URL, job.RetryCount, job.MaxRetries)

	err := d.doHTTPRequest(job)
	if err != nil {
		d.handleFailure(job, err)
		return
	}

	if err := d.store.MarkCompleted(job.ID); err != nil {
		log.Printf("[worker] mark completed error for job %d: %v", job.ID, err)
		return
	}
	log.Printf("[worker] job %d delivered successfully", job.ID)
}

func (d *Dispatcher) doHTTPRequest(job model.Job) error {
	var bodyReader io.Reader
	if job.Body != "" {
		bodyReader = strings.NewReader(job.Body)
	}

	req, err := http.NewRequest(job.Method, job.URL, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	for k, v := range job.Headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("unexpected status %d", resp.StatusCode)
}

func (d *Dispatcher) handleFailure(job model.Job, deliverErr error) {
	if job.RetryCount+1 >= job.MaxRetries {
		log.Printf("[worker] job %d exceeded max retries, marking failed: %v", job.ID, deliverErr)
		if err := d.store.MarkFailed(job.ID, deliverErr.Error()); err != nil {
			log.Printf("[worker] mark failed error for job %d: %v", job.ID, err)
		}
		return
	}

	nextRetry := time.Now().UTC().Add(model.BackoffDuration(job.RetryCount))
	log.Printf("[worker] job %d failed (%v), scheduling retry at %s",
		job.ID, deliverErr, nextRetry.Format(time.DateTime))

	if err := d.store.MarkRetry(job.ID, nextRetry); err != nil {
		log.Printf("[worker] mark retry error for job %d: %v", job.ID, err)
	}
}
