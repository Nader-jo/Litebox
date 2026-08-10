package repository

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/Nader-jo/Litebox/internal/model"
)

// SystemStats returns inexpensive, secret-free operational measurements.
func (r *Repository) SystemStats(ctx context.Context, databasePath string) (model.SystemStats, error) {
	var stats model.SystemStats
	err := r.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status IN ('queued', 'failed', 'running') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status = 'dead' THEN 1 ELSE 0 END), 0) FROM jobs`).Scan(&stats.PendingJobs, &stats.DeadJobs)
	if err != nil {
		return stats, err
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM attachments WHERE storage_status != 'ready'").Scan(&stats.FailedAttachments); err != nil {
		return stats, err
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE ingest_status = 'ready_without_raw'").Scan(&stats.MissingRawMessages); err != nil {
		return stats, err
	}
	var lastWebhook, lastIngest sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT MAX(received_at), MAX(CASE WHEN processing_status = 'succeeded' THEN processed_at END) FROM webhook_events").Scan(&lastWebhook, &lastIngest); err != nil {
		return stats, err
	}
	stats.LastWebhookAt, stats.LastSuccessfulIngest = nullableTime(lastWebhook), nullableTime(lastIngest)
	if info, err := os.Stat(databasePath); err == nil {
		stats.DatabaseBytes = info.Size()
	}
	if info, err := os.Stat(databasePath + "-wal"); err == nil {
		stats.WALBytes = info.Size()
	}
	return stats, nil
}

// ReferencedBlobs lists every logical object required by the SQLite state.
func (r *Repository) ReferencedBlobs(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT storage_key, sha256 FROM attachments
		WHERE storage_status = 'ready' AND storage_key != ''
		UNION ALL SELECT raw_storage_key, '' FROM messages WHERE raw_storage_key IS NOT NULL AND raw_storage_key != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var key, checksum string
		if err := rows.Scan(&key, &checksum); err != nil {
			return nil, err
		}
		result[key] = checksum
	}
	return result, rows.Err()
}

// Reindex rebuilds the FTS table from normalized message rows.
func (r *Repository) Reindex(ctx context.Context) error {
	return r.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM message_search"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO message_search(message_id, thread_id, subject, sender, recipients, body)
            SELECT m.id, m.thread_id, m.subject, m.from_name || ' ' || m.from_address,
              COALESCE((SELECT GROUP_CONCAT(address, ' ') FROM message_recipients mr WHERE mr.message_id = m.id), ''),
              m.text_body FROM messages m WHERE m.ingest_status IN ('ready', 'ready_without_raw')`)
		return err
	})
}

// LastBackupTimestamp is reserved for future backup history; zero means unknown.
func (r *Repository) LastBackupTimestamp(context.Context) time.Time { return time.Time{} }
