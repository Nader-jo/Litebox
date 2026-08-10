// Package jobs runs durable SQLite jobs with bounded leases and retry backoff.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/repository"
)

// Handler processes one claimed job and must be idempotent.
type Handler func(context.Context, model.Job) error

// PermanentError prevents a retry for invalid or unrecoverable work.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks an error as non-retryable.
func Permanent(err error) error { return &PermanentError{Err: err} }

// Runner owns a fixed-size worker pool.
type Runner struct {
	repository *repository.Repository
	handlers   map[string]Handler
	workers    int
	poll       time.Duration
	lease      time.Duration
	logger     *slog.Logger
	wait       sync.WaitGroup
}

// New creates a durable worker runner.
func New(repo *repository.Repository, handlers map[string]Handler, workers int, poll, lease time.Duration, logger *slog.Logger) *Runner {
	return &Runner{repository: repo, handlers: handlers, workers: workers, poll: poll, lease: lease, logger: logger}
}

// Start runs workers until context cancellation and returns immediately.
func (r *Runner) Start(ctx context.Context) {
	for index := 0; index < r.workers; index++ {
		r.wait.Add(1)
		go r.worker(ctx, fmt.Sprintf("worker-%d-%s", index+1, ids.New()[:8]))
	}
}

// Wait waits for all worker goroutines after cancellation.
func (r *Runner) Wait() { r.wait.Wait() }

func (r *Runner) worker(ctx context.Context, owner string) {
	defer r.wait.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		job, err := r.repository.ClaimJob(ctx, owner, r.lease)
		if errors.Is(err, repository.ErrNotFound) {
			timer.Reset(r.poll)
			continue
		}
		if err != nil {
			r.logger.Error("failed to claim job", "error", err)
			timer.Reset(r.poll)
			continue
		}
		handler, ok := r.handlers[job.Kind]
		if !ok {
			err = Permanent(fmt.Errorf("unsupported job kind"))
		} else {
			err = handler(ctx, job)
		}
		if err == nil {
			if completeErr := r.repository.CompleteJob(ctx, job.ID); completeErr != nil {
				r.logger.Error("failed to complete job", "job_id", job.ID, "kind", job.Kind, "error", completeErr)
			}
			timer.Reset(0)
			continue
		}
		var permanent *PermanentError
		isPermanent := errors.As(err, &permanent)
		retryAt := time.Now().Add(Backoff(job.AttemptCount))
		if failErr := r.repository.FailJob(ctx, job.ID, safeError(err), retryAt, isPermanent); failErr != nil {
			r.logger.Error("failed to record job error", "job_id", job.ID, "kind", job.Kind, "error", failErr)
		}
		r.logger.Warn("job failed", "job_id", job.ID, "kind", job.Kind, "attempt", job.AttemptCount, "permanent", isPermanent, "error", err)
		timer.Reset(0)
	}
}

// Backoff returns exponential retry delay with bounded jitter.
func Backoff(attempt int) time.Duration {
	steps := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	index := attempt - 1
	if index >= len(steps) {
		index = len(steps) - 1
	}
	base := steps[index]
	jitter := time.Duration(rand.Int64N(int64(base/5)+1)) - base/10
	return base + jitter
}

func safeError(err error) string {
	var permanent *PermanentError
	if errors.As(err, &permanent) {
		err = permanent.Err
	}
	type safe interface{ SafeMessage() string }
	var value safe
	if errors.As(err, &value) {
		return value.SafeMessage()
	}
	return "job processing failed; inspect structured server logs"
}
