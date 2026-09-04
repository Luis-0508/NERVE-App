package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound    = errors.New("inventory entity not found")
	ErrInvalidSort = errors.New("unsupported device sort")
)

type DBTX interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Repositories struct {
	Devices       *DeviceRepository
	Interfaces    *InterfaceRepository
	IPAddresses   *IPAddressRepository
	Services      *ServiceRepository
	Relationships *RelationshipRepository
}

func NewRepositories(db DBTX) *Repositories {
	return &Repositories{
		Devices:       &DeviceRepository{db: db},
		Interfaces:    &InterfaceRepository{db: db},
		IPAddresses:   &IPAddressRepository{db: db},
		Services:      &ServiceRepository{db: db},
		Relationships: &RelationshipRepository{db: db},
	}
}

type DeviceRepository struct{ db DBTX }
type InterfaceRepository struct{ db DBTX }
type IPAddressRepository struct{ db DBTX }
type ServiceRepository struct{ db DBTX }
type RelationshipRepository struct{ db DBTX }

func (r *DeviceRepository) List(ctx context.Context, params DeviceListParams) (DeviceList, error) {
	orderBy, ok := map[string]string{
		"name":           "lower(e.name)",
		"status":         "e.status",
		"classification": "d.classification",
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
		SELECT e.id::text, e.name, e.status, d.classification,
			COALESCE(d.hostname, ''), COALESCE(primary_ip.address::text, ''),
			COALESCE(primary_mac.address, ''), COALESCE(d.manufacturer, ''),
			COALESCE(d.operating_system, ''), e.first_seen_at, e.last_seen_at,
			count(*) OVER()
		FROM entities e
		JOIN devices d ON d.entity_id = e.id
		LEFT JOIN LATERAL (
			SELECT ip.address
			FROM network_interfaces ni
			JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
			JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
			WHERE ni.device_id = d.entity_id AND ip.valid_until IS NULL
			ORDER BY ip.is_primary DESC, ip.valid_from DESC
			LIMIT 1
		) primary_ip ON true
		LEFT JOIN LATERAL (
			SELECT ni.mac_address::text AS address
			FROM network_interfaces ni
			WHERE ni.device_id = d.entity_id AND ni.mac_address IS NOT NULL
			ORDER BY ni.is_up DESC NULLS LAST, ni.name
			LIMIT 1
		) primary_mac ON true
		WHERE ($1 = '' OR e.name ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.hostname, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(d.manufacturer, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(primary_ip.address::text, '') ILIKE '%%' || $1 || '%%'
			OR COALESCE(primary_mac.address, '') ILIKE '%%' || $1 || '%%')
			AND ($2 = '' OR e.status = $2)
			AND ($3 = '' OR d.classification = $3)
		ORDER BY %s %s NULLS LAST, e.id
		LIMIT $4 OFFSET $5`, orderBy, direction)

	rows, err := r.db.Query(ctx, query, params.Search, params.Status, params.Classification, params.Limit, params.Offset)
	if err != nil {
		return DeviceList{}, fmt.Errorf("query devices: %w", err)
	}
	defer rows.Close()

	result := DeviceList{Items: make([]DeviceSummary, 0), Limit: params.Limit, Offset: params.Offset}
	for rows.Next() {
		var item DeviceSummary
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Status, &item.Classification, &item.Hostname,
			&item.PrimaryIPAddress, &item.PrimaryMAC, &item.Vendor, &item.OperatingSystem,
			&item.FirstSeenAt, &item.LastSeenAt, &result.Total,
		); err != nil {
			return DeviceList{}, fmt.Errorf("scan device: %w", err)
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return DeviceList{}, fmt.Errorf("iterate devices: %w", err)
	}
	return result, nil
}

func (r *DeviceRepository) Get(ctx context.Context, id string) (Device, error) {
	var item Device
	err := r.db.QueryRow(ctx, `
		SELECT e.id::text, e.name, e.status, d.classification,
			COALESCE(d.hostname, ''), COALESCE(primary_ip.address::text, ''),
			COALESCE(primary_mac.address, ''), COALESCE(d.manufacturer, ''),
			COALESCE(d.operating_system, ''), e.first_seen_at, e.last_seen_at,
			COALESCE(d.model, ''), COALESCE(d.serial_number, ''), COALESCE(d.owner, ''),
			COALESCE(d.location, ''), COALESCE(d.notes, ''), d.approved_at
		FROM entities e
		JOIN devices d ON d.entity_id = e.id
		LEFT JOIN LATERAL (
			SELECT ip.address
			FROM network_interfaces ni
			JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
			JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
			WHERE ni.device_id = d.entity_id AND ip.valid_until IS NULL
			ORDER BY ip.is_primary DESC, ip.valid_from DESC
			LIMIT 1
		) primary_ip ON true
		LEFT JOIN LATERAL (
			SELECT ni.mac_address::text AS address
			FROM network_interfaces ni
			WHERE ni.device_id = d.entity_id AND ni.mac_address IS NOT NULL
			ORDER BY ni.is_up DESC NULLS LAST, ni.name
			LIMIT 1
		) primary_mac ON true
		WHERE e.id = $1::uuid`, id).Scan(
		&item.ID, &item.Name, &item.Status, &item.Classification, &item.Hostname,
		&item.PrimaryIPAddress, &item.PrimaryMAC, &item.Vendor, &item.OperatingSystem,
		&item.FirstSeenAt, &item.LastSeenAt, &item.Model, &item.SerialNumber,
		&item.Owner, &item.Location, &item.Notes, &item.ApprovedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("query device: %w", err)
	}
	return item, nil
}

func (r *DeviceRepository) Facets(ctx context.Context) (DeviceFacets, error) {
	rows, err := r.db.Query(ctx, `
		SELECT facet, value FROM (
			SELECT 'status' AS facet, e.status AS value
			FROM entities e JOIN devices d ON d.entity_id = e.id
			GROUP BY e.status
			UNION ALL
			SELECT 'classification' AS facet, d.classification AS value
			FROM devices d
			GROUP BY d.classification
		) facets
		ORDER BY facet, value`)
	if err != nil {
		return DeviceFacets{}, fmt.Errorf("query device facets: %w", err)
	}
	defer rows.Close()
	result := DeviceFacets{Statuses: make([]string, 0), Classifications: make([]string, 0)}
	for rows.Next() {
		var facet, value string
		if err := rows.Scan(&facet, &value); err != nil {
			return DeviceFacets{}, fmt.Errorf("scan device facet: %w", err)
		}
		if facet == "status" {
			result.Statuses = append(result.Statuses, value)
		} else {
			result.Classifications = append(result.Classifications, value)
		}
	}
	if err := rows.Err(); err != nil {
		return DeviceFacets{}, fmt.Errorf("iterate device facets: %w", err)
	}
	return result, nil
}

func (r *InterfaceRepository) ListByDevice(ctx context.Context, deviceID string) ([]Interface, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ni.entity_id::text, ni.name, COALESCE(ni.mac_address::text, ''),
			COALESCE(ni.media_type, ''), ni.mtu, ni.speed_bps, ni.is_up,
			ip.entity_id::text, ip.address::text, ip.version, ip.is_primary,
			ip.valid_from, ip.valid_until
		FROM network_interfaces ni
		LEFT JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
		LEFT JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
		WHERE ni.device_id = $1::uuid
		ORDER BY ni.name, ip.is_primary DESC NULLS LAST, ip.address`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query device interfaces: %w", err)
	}
	defer rows.Close()

	items := make([]Interface, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var interfaceID, name, macAddress, mediaType string
		var mtu sql.NullInt32
		var speed sql.NullInt64
		var isUp sql.NullBool
		var addressID, address sql.NullString
		var version sql.NullInt16
		var isPrimary sql.NullBool
		var validFrom, validUntil sql.NullTime
		if err := rows.Scan(&interfaceID, &name, &macAddress, &mediaType, &mtu, &speed, &isUp, &addressID, &address, &version, &isPrimary, &validFrom, &validUntil); err != nil {
			return nil, fmt.Errorf("scan device interface: %w", err)
		}
		index, exists := indexes[interfaceID]
		if !exists {
			item := Interface{ID: interfaceID, Name: name, MACAddress: macAddress, MediaType: mediaType, Addresses: make([]IPAddress, 0)}
			if mtu.Valid {
				value := mtu.Int32
				item.MTU = &value
			}
			if speed.Valid {
				value := speed.Int64
				item.SpeedBPS = &value
			}
			if isUp.Valid {
				value := isUp.Bool
				item.IsUp = &value
			}
			items = append(items, item)
			index = len(items) - 1
			indexes[interfaceID] = index
		}
		if addressID.Valid {
			ip := IPAddress{ID: addressID.String, Address: address.String, Version: version.Int16, IsPrimary: isPrimary.Bool, InterfaceID: interfaceID, InterfaceName: name, ValidFrom: validFrom.Time}
			if validUntil.Valid {
				value := validUntil.Time
				ip.ValidUntil = &value
			}
			items[index].Addresses = append(items[index].Addresses, ip)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device interfaces: %w", err)
	}
	return items, nil
}

func (r *IPAddressRepository) ListByDevice(ctx context.Context, deviceID string) ([]IPAddress, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ip.entity_id::text, ip.address::text, ip.version, ip.is_primary,
			ni.entity_id::text, ni.name, ip.valid_from, ip.valid_until
		FROM network_interfaces ni
		JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
		JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id
		WHERE ni.device_id = $1::uuid
		ORDER BY ip.valid_until NULLS FIRST, ip.is_primary DESC, ip.address`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query device IP addresses: %w", err)
	}
	defer rows.Close()
	items := make([]IPAddress, 0)
	for rows.Next() {
		var item IPAddress
		var validUntil sql.NullTime
		if err := rows.Scan(&item.ID, &item.Address, &item.Version, &item.IsPrimary, &item.InterfaceID, &item.InterfaceName, &item.ValidFrom, &validUntil); err != nil {
			return nil, fmt.Errorf("scan device IP address: %w", err)
		}
		if validUntil.Valid {
			value := validUntil.Time
			item.ValidUntil = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device IP addresses: %w", err)
	}
	return items, nil
}

func (r *ServiceRepository) ListByDevice(ctx context.Context, deviceID string) ([]ServiceEndpoint, error) {
	rows, err := r.db.Query(ctx, `
		SELECT endpoint.entity_id::text, COALESCE(service.entity_id::text, ''),
			COALESCE(service_entity.name, endpoint_entity.name), COALESCE(service.service_type, ''),
			endpoint.protocol, endpoint.port, endpoint.exposure, endpoint.state,
			endpoint.first_seen_at, endpoint.last_seen_at
		FROM service_endpoints endpoint
		JOIN entities endpoint_entity ON endpoint_entity.id = endpoint.entity_id
		LEFT JOIN services service ON service.entity_id = endpoint.service_id
		LEFT JOIN entities service_entity ON service_entity.id = service.entity_id
		WHERE endpoint.host_entity_id = $1::uuid
		ORDER BY endpoint.port, endpoint.protocol`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query device services: %w", err)
	}
	defer rows.Close()
	items := make([]ServiceEndpoint, 0)
	for rows.Next() {
		var item ServiceEndpoint
		if err := rows.Scan(&item.ID, &item.ServiceID, &item.Name, &item.ServiceType, &item.Protocol, &item.Port, &item.Exposure, &item.State, &item.FirstSeenAt, &item.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan device service: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device services: %w", err)
	}
	return items, nil
}

func (r *RelationshipRepository) ListByEntity(ctx context.Context, entityID string) ([]Relationship, error) {
	rows, err := r.db.Query(ctx, `
		SELECT relationship.id::text, relationship.relationship_type,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN 'outbound' ELSE 'inbound' END,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN target.id::text ELSE source.id::text END,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN target.name ELSE source.name END,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN target.kind ELSE source.kind END,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN target.status ELSE source.status END,
			relationship.source, relationship.confidence, relationship.first_seen_at,
			relationship.last_seen_at, relationship.manually_confirmed_at IS NOT NULL
		FROM relationships relationship
		JOIN entities source ON source.id = relationship.source_entity_id
		JOIN entities target ON target.id = relationship.target_entity_id
		WHERE relationship.source_entity_id = $1::uuid OR relationship.target_entity_id = $1::uuid
		ORDER BY relationship.relationship_type,
			CASE WHEN relationship.source_entity_id = $1::uuid THEN target.name ELSE source.name END`, entityID)
	if err != nil {
		return nil, fmt.Errorf("query entity relationships: %w", err)
	}
	defer rows.Close()
	items := make([]Relationship, 0)
	for rows.Next() {
		var item Relationship
		if err := rows.Scan(&item.ID, &item.Type, &item.Direction, &item.RelatedEntity.ID, &item.RelatedEntity.Name, &item.RelatedEntity.Kind, &item.RelatedEntity.Status, &item.Source, &item.Confidence, &item.FirstSeenAt, &item.LastSeenAt, &item.ManuallyConfirmed); err != nil {
			return nil, fmt.Errorf("scan entity relationship: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entity relationships: %w", err)
	}
	return items, nil
}

func NormalizeListParams(params DeviceListParams) (DeviceListParams, error) {
	params.Search = strings.TrimSpace(params.Search)
	params.Status = strings.TrimSpace(params.Status)
	params.Classification = strings.TrimSpace(params.Classification)
	params.Sort = strings.TrimSpace(params.Sort)
	params.Direction = strings.ToLower(strings.TrimSpace(params.Direction))
	if params.Sort == "" {
		params.Sort = "name"
	}
	if params.Direction == "" {
		params.Direction = "asc"
	}
	if params.Direction != "asc" && params.Direction != "desc" {
		return DeviceListParams{}, errors.New("direction must be asc or desc")
	}
	if len(params.Search) > 200 {
		return DeviceListParams{}, errors.New("search must not exceed 200 characters")
	}
	if params.Limit == 0 {
		params.Limit = 50
	}
	if params.Limit < 1 || params.Limit > 200 {
		return DeviceListParams{}, errors.New("limit must be between 1 and 200")
	}
	if params.Offset < 0 {
		return DeviceListParams{}, errors.New("offset must not be negative")
	}
	return params, nil
}
