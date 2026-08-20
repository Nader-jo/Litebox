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
		err, leaseLost := r.runJob(ctx, owner, job)
		if leaseLost {
			r.logger.Warn("job lease lost; stale result discarded", "job_id", job.ID, "kind", job.Kind, "owner", owner)
			if ctx.Err() != nil {
				return
			}
			timer.Reset(0)
			continue
		}
		if err == nil {
			finalizeContext, cancel := jobMutationContext(ctx)
			completeErr := r.repository.CompleteJob(finalizeContext, job.ID, owner)
			cancel()
			if errors.Is(completeErr, repository.ErrLeaseLost) {
				r.logger.Warn("job completion skipped after lease loss", "job_id", job.ID, "kind", job.Kind, "owner", owner)
			} else if completeErr != nil {
				r.logger.Error("failed to complete job", "job_id", job.ID, "kind", job.Kind, "error", completeErr)
			}
			if ctx.Err() != nil {
				return
			}
			timer.Reset(0)
			continue
		}
		if ctx.Err() != nil {
			r.logger.Info("job interrupted by shutdown; lease left for recovery", "job_id", job.ID, "kind", job.Kind, "owner", owner)
			return
		}
		var permanent *PermanentError
		isPermanent := errors.As(err, &permanent)
		retryAt := time.Now().Add(Backoff(job.AttemptCount))
		finalizeContext, cancel := jobMutationContext(ctx)
		failErr := r.repository.FailJob(finalizeContext, job.ID, owner, safeError(err), retryAt, isPermanent)
		cancel()
		if errors.Is(failErr, repository.ErrLeaseLost) {
			r.logger.Warn("job failure discarded after lease loss", "job_id", job.ID, "kind", job.Kind, "owner", owner)
		} else if failErr != nil {
			r.logger.Error("failed to record job error", "job_id", job.ID, "kind", job.Kind, "error", failErr)
		}
		r.logger.Warn("job failed", "job_id", job.ID, "kind", job.Kind, "attempt", job.AttemptCount, "permanent", isPermanent, "error", err)
		timer.Reset(0)
	}
}

func (r *Runner) runJob(ctx context.Context, owner string, job model.Job) (error, bool) {
	handlerContext, cancelHandler := context.WithCancel(ctx)
	defer cancelHandler()

	result := make(chan error, 1)
	go func() {
		handler, ok := r.handlers[job.Kind]
		if !ok {
			result <- Permanent(fmt.Errorf("unsupported job kind"))
			return
		}
		result <- handler(handlerContext, job)
	}()

	ticker := time.NewTicker(leaseHeartbeatInterval(r.lease))
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err, false
		case <-ctx.Done():
			cancelHandler()
			return <-result, false
		case <-ticker.C:
			err := r.repository.RenewJobLease(ctx, job.ID, owner, r.lease)
			if errors.Is(err, repository.ErrLeaseLost) {
				cancelHandler()
				return <-result, true
			}
			if err != nil {
				r.logger.Error("failed to renew job lease", "job_id", job.ID, "kind", job.Kind, "owner", owner, "error", err)
			}
		}
	}
}

func leaseHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Millisecond {
		return time.Millisecond
	}
	return interval
}

func jobMutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
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
