package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RecoverStaleScans clears jobs that can no longer be healthy. The agent has a
// hard upper scan timeout of six minutes, so seven minutes leaves a small grace
// period while ensuring the UI cannot remain stuck on an ancient running job.
// Queued jobs are expected to be claimed within seconds while the local agent is
// online; an old queued job is therefore also safe to fail and retry.
func (r *Repository) RecoverStaleScans(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		UPDATE discovery_scans
		SET status = 'failed',
			completed_at = now(),
			error_message = CASE
				WHEN status = 'queued' THEN 'Discovery scan was not claimed by an agent in time.'
				ELSE 'Discovery scan exceeded the maximum runtime and was stopped.'
			END
		WHERE (status = 'queued' AND requested_at < now() - interval '2 minutes')
		   OR (status = 'running' AND started_at < now() - interval '7 minutes')`)
	if err != nil {
		return fmt.Errorf("recover stale discovery scans: %w", err)
	}
	return nil
}

// CancelActiveScan stops the active job for a target. A running agent will see
// the job become non-running on its next live report and abort that scan. The
// existing status enum predates an explicit cancelled state, so cancellation is
// represented as failed with a clear operator-facing reason until the discovery
// lifecycle schema is expanded in a later milestone.
func (r *Repository) CancelActiveScan(ctx context.Context, targetID, userID string) (Scan, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Scan{}, fmt.Errorf("begin discovery cancellation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var scanID string
	err = tx.QueryRow(ctx, `
		UPDATE discovery_scans
		SET status = 'failed', completed_at = now(), error_message = 'Discovery scan cancelled by administrator.'
		WHERE target_id = $1::uuid AND status IN ('queued', 'running')
		RETURNING id::text`, targetID).Scan(&scanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scan{}, ErrNoJob
	}
	if err != nil {
		return Scan{}, fmt.Errorf("cancel discovery scan: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO events (type, severity, category, message, details)
		VALUES ('discovery.scan.cancelled', 'info', 'discovery', 'Local network discovery was cancelled by an administrator.',
			jsonb_build_object('scanId', $1::text, 'requestedBy', $2::text))`, scanID, userID); err != nil {
		return Scan{}, fmt.Errorf("record discovery cancellation event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Scan{}, fmt.Errorf("commit discovery cancellation: %w", err)
	}

	scan, err := r.latestScan(ctx, targetID)
	if err != nil {
		return Scan{}, fmt.Errorf("read cancelled discovery scan: %w", err)
	}
	return scan, nil
}
