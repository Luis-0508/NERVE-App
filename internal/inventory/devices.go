package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// deviceColumns selects the effective device: the operator's display name and
// classification override win over discovered values. entities.name already
// holds the effective name (kept in sync by database triggers).
const deviceColumns = `e.id::text, e.name, COALESCE(d.display_name, ''), COALESCE(d.discovered_name, ''), e.status,
			COALESCE(d.classification_override, d.classification), d.classification, d.classification_override IS NOT NULL,
			d.trust_state, ARRAY(SELECT tag FROM device_tags t WHERE t.device_id = d.entity_id ORDER BY tag),
			COALESCE(d.hostname, ''), COALESCE(host(primary_ip.address), ''),
			COALESCE(primary_mac.address, ''), COALESCE(d.manufacturer, ''),
			COALESCE(d.operating_system, ''), e.first_seen_at, e.last_seen_at`

// primaryJoins picks one current address and MAC for display. Identity does
// not depend on them; the identity evidence endpoint has the full picture.
const primaryJoins = `
		LEFT JOIN LATERAL (
			SELECT ip.address
			FROM network_interfaces ni
			JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
			JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
			WHERE ni.device_id = d.entity_id AND ip.valid_until IS NULL
			ORDER BY ip.is_primary DESC, ip.valid_from DESC, ip.address
			LIMIT 1
		) primary_ip ON true
		LEFT JOIN LATERAL (
			SELECT ni.mac_address::text AS address
			FROM network_interfaces ni
			WHERE ni.device_id = d.entity_id AND ni.mac_address IS NOT NULL
			ORDER BY ni.is_up DESC NULLS LAST, ni.name
			LIMIT 1
		) primary_mac ON true`

// deviceSearch matches human-owned and discovered names, vendor, every address
// the device has held, every MAC and tags. %% escapes Sprintf.
const deviceSearch = `($1 = '' OR e.name ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.display_name, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.discovered_name, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.hostname, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.manufacturer, '') ILIKE '%%' || $1 || '%%'
			OR EXISTS (SELECT 1 FROM device_tags t WHERE t.device_id = d.entity_id AND t.tag ILIKE '%%' || $1 || '%%')
			OR EXISTS (SELECT 1 FROM network_interfaces ni
				LEFT JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
				LEFT JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
				WHERE ni.device_id = d.entity_id
				AND (COALESCE(ni.mac_address::text, '') ILIKE '%%' || $1 || '%%'
					OR COALESCE(host(ip.address), '') ILIKE '%%' || $1 || '%%')))`

func scanSummary(row pgx.Row, item *DeviceSummary, extra ...any) error {
	destinations := append([]any{&item.ID, &item.Name, &item.DisplayName, &item.DiscoveredName, &item.Status,
		&item.Classification, &item.DiscoveredClassification, &item.ClassificationOverridden, &item.TrustState, &item.Tags,
		&item.Hostname, &item.PrimaryIPAddress, &item.PrimaryMAC, &item.Vendor, &item.OperatingSystem,
		&item.FirstSeenAt, &item.LastSeenAt}, extra...)
	if err := row.Scan(destinations...); err != nil {
		return err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	return nil
}

// List returns current devices; devices retired by a merge are hidden.
func (r *DeviceRepository) List(ctx context.Context, params DeviceListParams) (DeviceList, error) {
	orderBy, ok := map[string]string{
		"name":           "lower(e.name)",
		"status":         "e.status",
		"classification": "COALESCE(d.classification_override, d.classification)",
		"trust":          "d.trust_state",
		"hostname":       "lower(COALESCE(d.hostname, ''))",
		"ip":             "primary_ip.address",
		"vendor":         "lower(COALESCE(d.manufacturer, ''))",
		"first_seen":     "e.first_seen_at",
		"last_seen":      "e.last_seen_at",
	}[params.Sort]
	if !ok {
		return DeviceList{}, ErrInvalidSort
	}
	direction := "ASC"
	if params.Direction == "desc" {
		direction = "DESC"
	}
	query := fmt.Sprintf(`
		SELECT `+deviceColumns+`, count(*) OVER()
		FROM entities e
		JOIN devices d ON d.entity_id = e.id`+primaryJoins+`
		WHERE d.merged_into IS NULL
			AND `+deviceSearch+`
			AND ($2 = '' OR e.status = $2)
			AND ($3 = '' OR COALESCE(d.classification_override, d.classification) = $3)
			AND ($6 = '' OR d.trust_state = $6)
			AND ($7 = '' OR EXISTS (SELECT 1 FROM device_tags t WHERE t.device_id = d.entity_id AND t.tag = $7))
		ORDER BY %s %s NULLS LAST, e.id
		LIMIT $4 OFFSET $5`, orderBy, direction)

	rows, err := r.db.Query(ctx, query, params.Search, params.Status, params.Classification, params.Limit, params.Offset, params.Trust, params.Tag)
	if err != nil {
		return DeviceList{}, fmt.Errorf("query devices: %w", err)
	}
	defer rows.Close()

	result := DeviceList{Items: make([]DeviceSummary, 0), Limit: params.Limit, Offset: params.Offset}
	for rows.Next() {
		var item DeviceSummary
		if err := scanSummary(rows, &item, &result.Total); err != nil {
			return DeviceList{}, fmt.Errorf("scan device: %w", err)
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return DeviceList{}, fmt.Errorf("iterate devices: %w", err)
	}
	return result, nil
}

// Get returns a device, including retired (merged) ones so old links keep
// working; MergedInto tells the client where the record now lives.
func (r *DeviceRepository) Get(ctx context.Context, id string) (Device, error) {
	var item Device
	err := scanSummary(r.db.QueryRow(ctx, `
		SELECT `+deviceColumns+`,
			COALESCE(d.model, ''), COALESCE(d.serial_number, ''), COALESCE(d.owner, ''),
			COALESCE(d.location, ''), COALESCE(d.notes, ''), d.approved_at,
			d.metadata_updated_at, COALESCE(metadata_user.email, ''), d.trust_changed_at, COALESCE(trust_user.email, ''),
			COALESCE(d.merged_into::text, '')
		FROM entities e
		JOIN devices d ON d.entity_id = e.id
		LEFT JOIN users metadata_user ON metadata_user.id = d.metadata_updated_by
		LEFT JOIN users trust_user ON trust_user.id = d.trust_changed_by`+primaryJoins+`
		WHERE e.id = $1::uuid`, id), &item.DeviceSummary,
		&item.Model, &item.SerialNumber, &item.Owner, &item.Location, &item.Notes, &item.ApprovedAt,
		&item.MetadataUpdatedAt, &item.MetadataUpdatedBy, &item.TrustChangedAt, &item.TrustChangedBy, &item.MergedInto,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("query device: %w", err)
	}
	return item, nil
}

// Facets lists the filter values present among current devices.
func (r *DeviceRepository) Facets(ctx context.Context) (DeviceFacets, error) {
	rows, err := r.db.Query(ctx, `
		SELECT facet, value FROM (
			SELECT 'status' AS facet, e.status AS value
			FROM entities e JOIN devices d ON d.entity_id = e.id WHERE d.merged_into IS NULL
			GROUP BY e.status
			UNION ALL
			SELECT 'classification', COALESCE(d.classification_override, d.classification)
			FROM devices d WHERE d.merged_into IS NULL
			GROUP BY 2
			UNION ALL
			SELECT 'trust', d.trust_state
			FROM devices d WHERE d.merged_into IS NULL
			GROUP BY 2
			UNION ALL
			SELECT 'tag', t.tag
			FROM device_tags t JOIN devices d ON d.entity_id = t.device_id WHERE d.merged_into IS NULL
			GROUP BY 2
		) facets
		ORDER BY facet, value`)
	if err != nil {
		return DeviceFacets{}, fmt.Errorf("query device facets: %w", err)
	}
	defer rows.Close()
	result := DeviceFacets{Statuses: []string{}, Classifications: []string{}, TrustStates: []string{}, Tags: []string{}}
	for rows.Next() {
		var facet, value string
		if err := rows.Scan(&facet, &value); err != nil {
			return DeviceFacets{}, fmt.Errorf("scan device facet: %w", err)
		}
		switch facet {
		case "status":
			result.Statuses = append(result.Statuses, value)
		case "classification":
			result.Classifications = append(result.Classifications, value)
		case "trust":
			result.TrustStates = append(result.TrustStates, value)
		case "tag":
			result.Tags = append(result.Tags, value)
		}
	}
	if err := rows.Err(); err != nil {
		return DeviceFacets{}, fmt.Errorf("iterate device facets: %w", err)
	}
	return result, nil
}
