// Package repository contains all SQL used by Litebox application services.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

// ErrNotFound is returned when a requested domain record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrAttachmentLimit reports that a concurrent attachment reservation would
// exceed the configured count or byte budget.
var ErrAttachmentLimit = errors.New("attachment limit exceeded")

// Repository is a concurrency-safe collection of SQLite operations.
type Repository struct{ db *sql.DB }

// New creates a repository over an initialized database.
func New(database *sql.DB) *Repository { return &Repository{db: database} }

// DB exposes the connection pool only to operational commands such as backup.
func (r *Repository) DB() *sql.DB { return r.db }

func millis(value time.Time) int64 { return value.UTC().UnixMilli() }

func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := fromMillis(value.Int64)
	return &parsed
}

func boolean(value int) bool { return value != 0 }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rollback(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("rollback: %w", err))
	}
	return cause
}

// Ping checks database readiness.
func (r *Repository) Ping(ctx context.Context) error { return r.db.PingContext(ctx) }

// Transaction runs work atomically.
func (r *Repository) Transaction(ctx context.Context, work func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := work(tx); err != nil {
		return rollback(tx, err)
	}
	return tx.Commit()
}

// ScanJob converts a row into a durable job.
func ScanJob(scanner interface{ Scan(...any) error }) (model.Job, error) {
	var job model.Job
	var runAfter, createdAt, updatedAt int64
	var leasedUntil sql.NullInt64
	var dedupe, leaseOwner, lastError sql.NullString
	err := scanner.Scan(&job.ID, &job.Kind, &dedupe, &job.PayloadJSON, &job.Status, &job.Priority,
		&job.AttemptCount, &job.MaxAttempts, &runAfter, &leasedUntil, &leaseOwner, &lastError, &createdAt, &updatedAt)
	job.DedupeKey = dedupe.String
	job.LeaseOwner = leaseOwner.String
	job.LastError = lastError.String
	job.RunAfter = fromMillis(runAfter)
	job.LeasedUntil = nullableTime(leasedUntil)
	job.CreatedAt = fromMillis(createdAt)
	job.UpdatedAt = fromMillis(updatedAt)
	return job, err
}
