package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxListOffset bounds paging so a request cannot make PostgreSQL skip an
// arbitrarily large result range.
const maxListOffset = 100000

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

func (r *InterfaceRepository) ListByDevice(ctx context.Context, deviceID string) ([]Interface, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ni.entity_id::text, ni.name, COALESCE(ni.mac_address::text, ''),
			COALESCE(ni.media_type, ''), ni.mtu, ni.speed_bps, ni.is_up,
			ip.entity_id::text, host(ip.address), ip.version, ip.is_primary,
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
		SELECT ip.entity_id::text, host(ip.address), ip.version, ip.is_primary,
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
	params.Trust = strings.TrimSpace(params.Trust)
	params.Tag = strings.ToLower(strings.TrimSpace(params.Tag))
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
	if params.Offset > maxListOffset {
		return DeviceListParams{}, fmt.Errorf("offset must not exceed %d; narrow the search instead", maxListOffset)
	}
	return params, nil
}
