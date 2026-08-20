package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Nader-jo/Litebox/internal/ids"
	"github.com/Nader-jo/Litebox/internal/model"
)

const jobColumns = `id, kind, dedupe_key, payload_json, status, priority, attempt_count,
    max_attempts, run_after, leased_until, lease_owner, last_error, created_at, updated_at`

// ErrLeaseLost means a worker no longer owns the running job it attempted to
// renew or finalize. Callers must not retry the mutation without claiming the
// job again.
var ErrLeaseLost = errors.New("job lease lost")

// EnqueueJob creates durable work and quietly deduplicates an existing logical job.
func (r *Repository) EnqueueJob(ctx context.Context, kind, dedupeKey, payload string, priority, maxAttempts int, runAfter time.Time) (string, bool, error) {
	return enqueueJob(ctx, r.db, kind, dedupeKey, payload, priority, maxAttempts, runAfter)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueJob(ctx context.Context, db executor, kind, dedupeKey, payload string, priority, maxAttempts int, runAfter time.Time) (string, bool, error) {
	id := ids.New()
	now := millis(time.Now())
	var nullableDedupe any
	if dedupeKey != "" {
		nullableDedupe = dedupeKey
	}
	result, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO jobs
        (id, kind, dedupe_key, payload_json, status, priority, attempt_count, max_attempts, run_after, created_at, updated_at)
        VALUES (?, ?, ?, ?, 'queued', ?, 0, ?, ?, ?, ?)`, id, kind, nullableDedupe, payload, priority, maxAttempts, millis(runAfter), now, now)
	if err != nil {
		return "", false, err
	}
	count, err := result.RowsAffected()
	return id, count > 0, err
}

// ClaimJob atomically reclaims expired leases and leases one runnable job.
func (r *Repository) ClaimJob(ctx context.Context, worker string, lease time.Duration) (model.Job, error) {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', lease_owner = NULL, leased_until = NULL, updated_at = ?
        WHERE status = 'running' AND leased_until IS NOT NULL AND leased_until < ?`, millis(now), millis(now))
	if err != nil {
		return model.Job{}, err
	}
	row := r.db.QueryRowContext(ctx, `UPDATE jobs SET
        status = 'running', lease_owner = ?, leased_until = ?, attempt_count = attempt_count + 1, updated_at = ?
        WHERE id = (
            SELECT id FROM jobs WHERE status IN ('queued', 'failed') AND run_after <= ?
            ORDER BY priority ASC, created_at ASC LIMIT 1
        )
        RETURNING `+jobColumns, worker, millis(now.Add(lease)), millis(now), millis(now))
	job, err := ScanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	return job, err
}

// RenewJobLease extends a running job lease only while owner still holds it.
func (r *Repository) RenewJobLease(ctx context.Context, id, owner string, lease time.Duration) error {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET leased_until = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_owner = ?`,
		millis(now.Add(lease)), millis(now), id, owner)
	return fencedJobMutation(result, err)
}

// CompleteJob records a successful terminal job state only while owner still
// holds the running lease.
func (r *Repository) CompleteJob(ctx context.Context, id, owner string) error {
	now := millis(time.Now())
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET status = 'succeeded', lease_owner = NULL,
		leased_until = NULL, completed_at = ?, updated_at = ?, last_error = NULL
		WHERE id = ? AND status = 'running' AND lease_owner = ?`, now, now, id, owner)
	return fencedJobMutation(result, err)
}

// FailJob records an error and either reschedules or dead-letters the job only
// while owner still holds the running lease.
func (r *Repository) FailJob(ctx context.Context, id, owner, message string, retryAt time.Time, permanent bool) error {
	status := "failed"
	if permanent {
		status = "dead"
	}
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET status = CASE
			WHEN ? = 'dead' OR attempt_count >= max_attempts THEN 'dead' ELSE 'failed' END,
		run_after = ?, lease_owner = NULL, leased_until = NULL, last_error = ?, updated_at = ?
		WHERE id = ? AND status = 'running' AND lease_owner = ?`,
		status, millis(retryAt), message, millis(time.Now()), id, owner)
	return fencedJobMutation(result, err)
}

func fencedJobMutation(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ListJobs returns recent jobs for diagnostics.
func (r *Repository) ListJobs(ctx context.Context, limit int) ([]model.Job, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs
        ORDER BY CASE status WHEN 'dead' THEN 0 WHEN 'failed' THEN 1 WHEN 'running' THEN 2 ELSE 3 END, updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []model.Job
	for rows.Next() {
		job, err := ScanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// RetryJob returns a failed/dead job to the queue without erasing its history.
func (r *Repository) RetryJob(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', run_after = ?, lease_owner = NULL,
        leased_until = NULL, last_error = NULL, updated_at = ? WHERE id = ? AND status IN ('failed', 'dead')`,
		millis(time.Now()), millis(time.Now()), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}
