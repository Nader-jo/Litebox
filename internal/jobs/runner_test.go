package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nader-jo/Litebox/internal/db"
	"github.com/Nader-jo/Litebox/internal/model"
	"github.com/Nader-jo/Litebox/internal/repository"
)

func TestBackoffIsBounded(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 12; attempt++ {
		value := Backoff(attempt)
		if value < 9*time.Second || value > 7*time.Hour {
			t.Fatalf("attempt %d produced invalid backoff %s", attempt, value)
		}
		if attempt > 1 && value < previous/3 {
			t.Fatalf("backoff unexpectedly regressed from %s to %s", previous, value)
		}
		previous = value
	}
}

func TestRunnerRenewsLeaseWhileHandlerRuns(t *testing.T) {
	repo := runnerTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan model.Job, 1)
	release := make(chan struct{})
	runner := New(repo, map[string]Handler{
		"long": func(ctx context.Context, job model.Job) error {
			started <- job
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, 1, 5*time.Millisecond, 600*time.Millisecond, discardLogger())
	runner.Start(ctx)
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})

	id, inserted, err := repo.EnqueueJob(ctx, "long", "runner:renew", `{}`, 100, 3, time.Now())
	if err != nil || !inserted {
		t.Fatal(id, inserted, err)
	}
	var claimed model.Job
	select {
	case claimed = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	if claimed.LeasedUntil == nil {
		t.Fatal("claimed job has no lease expiry")
	}
	initialExpiry := *claimed.LeasedUntil
	renewed := waitForRunnerJob(t, repo, func(job model.Job) bool {
		return job.ID == id && job.Status == "running" && job.LeasedUntil != nil && job.LeasedUntil.After(initialExpiry)
	})
	if renewed.LeaseOwner == "" || renewed.AttemptCount != 1 {
		t.Fatalf("unexpected renewed job: %#v", renewed)
	}

	if delay := time.Until(initialExpiry.Add(50 * time.Millisecond)); delay > 0 {
		timer := time.NewTimer(delay)
		<-timer.C
	}
	if _, err := repo.ClaimJob(context.Background(), "competing-worker", time.Minute); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("renewed job was reclaimed after its original expiry: %v", err)
	}

	close(release)
	completed := waitForRunnerJob(t, repo, func(job model.Job) bool { return job.ID == id && job.Status == "succeeded" })
	if completed.AttemptCount != 1 || completed.LeaseOwner != "" || completed.LeasedUntil != nil {
		t.Fatalf("unexpected completed job: %#v", completed)
	}
}

func TestRunnerCancelsHandlerAndDiscardsResultAfterLeaseLoss(t *testing.T) {
	repo := runnerTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan model.Job, 1)
	handlerCanceled := make(chan struct{})
	runner := New(repo, map[string]Handler{
		"long": func(ctx context.Context, job model.Job) error {
			started <- job
			<-ctx.Done()
			close(handlerCanceled)
			return ctx.Err()
		},
	}, 1, 5*time.Millisecond, 90*time.Millisecond, discardLogger())
	runner.Start(ctx)
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})

	id, inserted, err := repo.EnqueueJob(ctx, "long", "runner:lease-loss", `{}`, 100, 3, time.Now())
	if err != nil || !inserted {
		t.Fatal(id, inserted, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	if _, err := repo.DB().ExecContext(context.Background(), `UPDATE jobs SET lease_owner = ?, leased_until = ?
		WHERE id = ? AND status = 'running'`, "replacement-worker", time.Now().Add(time.Hour).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not canceled after lease loss")
	}
	job := waitForRunnerJob(t, repo, func(job model.Job) bool {
		return job.ID == id && job.Status == "running" && job.LeaseOwner == "replacement-worker"
	})
	if job.AttemptCount != 1 || job.LastError != "" {
		t.Fatalf("stale worker changed replacement job: %#v", job)
	}
}

func TestRunnerLeavesInterruptedJobForLeaseRecoveryOnShutdown(t *testing.T) {
	repo := runnerTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	runner := New(repo, map[string]Handler{
		"long": func(ctx context.Context, _ model.Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}, 1, 5*time.Millisecond, time.Minute, discardLogger())
	runner.Start(ctx)
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})

	id, inserted, err := repo.EnqueueJob(ctx, "long", "runner:shutdown", `{}`, 100, 3, time.Now())
	if err != nil || !inserted {
		t.Fatal(id, inserted, err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	runner.Wait()
	job := waitForRunnerJob(t, repo, func(job model.Job) bool { return job.ID == id })
	if job.Status != "running" || job.LeaseOwner == "" || job.AttemptCount != 1 || job.LastError != "" {
		t.Fatalf("shutdown converted interruption into a job failure: %#v", job)
	}
}

func runnerTestRepository(t *testing.T) *repository.Repository {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "mailbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return repository.New(database)
}

func waitForRunnerJob(t *testing.T, repo *repository.Repository, match func(model.Job) bool) model.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := repo.ListJobs(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if match(job) {
				return job
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for job state")
	return model.Job{}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
