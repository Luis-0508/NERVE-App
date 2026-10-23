package devices

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Default confidence per source. Agent facts come from an authenticated,
// enrolled endpoint describing itself; reverse DNS describes what the network
// resolver says about an address and is often stale or generic. An OUI vendor
// names who was assigned the MAC block, which for virtual or replacement
// network adapters is not the device's maker.
const (
	ConfidenceOUI        = 40
	ConfidenceReverseDNS = 50
	ConfidenceAgent      = 85
)

// discoveredColumns maps provenance attributes to the device column that holds
// the effective discovered value.
var discoveredColumns = map[string]string{
	"hostname":         "hostname",
	"operating_system": "operating_system",
	"manufacturer":     "manufacturer",
	"model":            "model",
	"serial_number":    "serial_number",
}

// RecordAttribute stores one sourced claim about a device and refreshes the
// device's discovered values. It never touches human-owned fields.
func RecordAttribute(ctx context.Context, tx pgx.Tx, deviceID, attribute, source, value string, confidence int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if runes := []rune(value); len(runes) > 256 {
		value = string(runes[:256])
	}
	if _, err := tx.Exec(ctx, `INSERT INTO device_attributes (device_id, attribute, source, value, confidence)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (device_id, attribute, source) DO UPDATE SET
			value = EXCLUDED.value, confidence = EXCLUDED.confidence, last_seen_at = now(),
			first_seen_at = CASE WHEN device_attributes.value = EXCLUDED.value THEN device_attributes.first_seen_at ELSE now() END`,
		deviceID, attribute, source, value, confidence); err != nil {
		return fmt.Errorf("record device attribute %s: %w", attribute, err)
	}
	return RefreshDiscovered(ctx, tx, deviceID)
}

// RefreshDiscovered recomputes the discovered columns from the most confident
// (then most recent) claim per attribute. The discovered name follows the
// effective hostname; entities.name follows via trigger unless an operator
// has set a display name.
func RefreshDiscovered(ctx context.Context, tx pgx.Tx, deviceID string) error {
	assignments := make([]string, 0, len(discoveredColumns)+1)
	for attribute, column := range discoveredColumns {
		assignments = append(assignments, fmt.Sprintf(`%s = COALESCE((SELECT value FROM device_attributes
			WHERE device_id = d.entity_id AND attribute = '%s' ORDER BY confidence DESC, last_seen_at DESC LIMIT 1), d.%s)`,
			column, attribute, column))
	}
	assignments = append(assignments, `discovered_name = COALESCE((SELECT value FROM device_attributes
		WHERE device_id = d.entity_id AND attribute = 'hostname' ORDER BY confidence DESC, last_seen_at DESC LIMIT 1), d.discovered_name)`)
	if _, err := tx.Exec(ctx, `UPDATE devices d SET `+strings.Join(assignments, ", ")+` WHERE d.entity_id = $1::uuid`, deviceID); err != nil {
		return fmt.Errorf("refresh discovered device facts: %w", err)
	}
	return nil
}

// Querier is satisfied by pgx.Tx and pgxpool.Pool.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ListAttributes returns all sourced claims, marking the effective one per attribute.
func ListAttributes(ctx context.Context, db Querier, deviceID string) ([]Attribute, error) {
	rows, err := db.Query(ctx, `SELECT attribute, source, value, confidence, first_seen_at, last_seen_at,
		row_number() OVER (PARTITION BY attribute ORDER BY confidence DESC, last_seen_at DESC) = 1
		FROM device_attributes WHERE device_id = $1::uuid ORDER BY attribute, confidence DESC, last_seen_at DESC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query device attributes: %w", err)
	}
	defer rows.Close()
	items := make([]Attribute, 0)
	for rows.Next() {
		var item Attribute
		if err := rows.Scan(&item.Attribute, &item.Source, &item.Value, &item.Confidence, &item.FirstSeenAt, &item.LastSeenAt, &item.Effective); err != nil {
			return nil, fmt.Errorf("scan device attribute: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ForgetSource removes every claim one source made about a device (for
// example when its management agent is detached or moved) and recomputes the
// discovered values from what remains. Unlike RefreshDiscovered, columns with
// no remaining claim are cleared rather than kept.
func ForgetSource(ctx context.Context, tx pgx.Tx, deviceID, source string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM device_identities WHERE device_id = $1::uuid AND source = $2 AND identity_type <> 'agent'`, deviceID, source); err != nil {
		return fmt.Errorf("forget %s identities: %w", source, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM device_attributes WHERE device_id = $1::uuid AND source = $2`, deviceID, source); err != nil {
		return fmt.Errorf("forget %s facts: %w", source, err)
	}
	assignments := make([]string, 0, len(discoveredColumns)+1)
	for attribute, column := range discoveredColumns {
		assignments = append(assignments, fmt.Sprintf(`%s = (SELECT value FROM device_attributes
			WHERE device_id = d.entity_id AND attribute = '%s' ORDER BY confidence DESC, last_seen_at DESC LIMIT 1)`, column, attribute))
	}
	assignments = append(assignments, `discovered_name = COALESCE(
		(SELECT value FROM device_attributes WHERE device_id = d.entity_id AND attribute = 'hostname' ORDER BY confidence DESC, last_seen_at DESC LIMIT 1),
		(SELECT host(ip.address) FROM network_interfaces ni JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
			JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
			WHERE ni.device_id = d.entity_id AND ip.valid_until IS NULL ORDER BY ip.valid_from DESC LIMIT 1),
		d.discovered_name)`)
	if _, err := tx.Exec(ctx, `UPDATE devices d SET `+strings.Join(assignments, ", ")+` WHERE d.entity_id = $1::uuid`, deviceID); err != nil {
		return fmt.Errorf("recompute discovered device facts: %w", err)
	}
	return nil
}
