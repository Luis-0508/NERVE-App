package devices

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/oui"
)

// RecordVendor records the manufacturer the embedded IEEE registry assigns to
// one of the device's MAC addresses, as a low-confidence `oui` claim that any
// self-description by an enrolled agent outranks. A MAC observed on the
// network by discovery is preferred over adapters only an agent reports,
// since agents also list virtual and secondary adapters. It reports whether a
// vendor was found.
func RecordVendor(ctx context.Context, tx pgx.Tx, deviceID string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT value FROM device_identities
		WHERE device_id = $1::uuid AND identity_type = 'mac'
		ORDER BY source = 'local-discovery' DESC, confidence DESC, last_seen_at DESC, value`, deviceID)
	if err != nil {
		return false, fmt.Errorf("query device MAC addresses: %w", err)
	}
	macs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("scan device MAC addresses: %w", err)
	}
	for _, mac := range macs {
		if vendor, ok := oui.Lookup(mac); ok {
			return true, RecordAttribute(ctx, tx, deviceID, "manufacturer", SourceOUI, vendor, ConfidenceOUI)
		}
	}
	return false, nil
}

// BackfillVendors runs RecordVendor for every live device that has a MAC
// address but no `oui` claim yet, so devices recorded before the lookup
// existed gain a vendor. Devices that already have one are skipped, which
// keeps repeated runs cheap; their claim is refreshed on the next observation.
func BackfillVendors(ctx context.Context, db *pgxpool.Pool) (int, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT identity.device_id::text FROM device_identities identity
		JOIN devices device ON device.entity_id = identity.device_id AND device.merged_into IS NULL
		WHERE identity.identity_type = 'mac' AND NOT EXISTS (SELECT 1 FROM device_attributes attribute
			WHERE attribute.device_id = identity.device_id AND attribute.attribute = 'manufacturer' AND attribute.source = $1)`, SourceOUI)
	if err != nil {
		return 0, fmt.Errorf("query devices without a vendor: %w", err)
	}
	deviceIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("scan devices without a vendor: %w", err)
	}
	recorded := 0
	var failures error
	for _, deviceID := range deviceIDs {
		found := false
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) (err error) {
			found, err = RecordVendor(ctx, tx, deviceID)
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return recorded, ctx.Err()
			}
			failures = errors.Join(failures, fmt.Errorf("device %s: %w", deviceID, err))
		} else if found {
			recorded++
		}
	}
	return recorded, failures
}
