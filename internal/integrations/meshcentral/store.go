package meshcentral

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/audit"
	"github.com/nerve-labs/nerve/internal/remote"
)

// ProviderName is the remote-management provider this adapter registers as.
const ProviderName = "meshcentral"

var (
	ErrNodeNotFound   = errors.New("meshcentral node not found")
	ErrDeviceNotFound = errors.New("device not found")
	ErrAlreadyBound   = errors.New("meshcentral node is already bound to a device")
	ErrNotBound       = errors.New("meshcentral node is not bound to this device")
	// ErrWeakEvidence refuses a binding that no MAC address supports.
	ErrWeakEvidence = errors.New("binding needs a matching MAC address")
)

// Store persists synced nodes and operator-confirmed bindings.
type Store struct {
	db     *pgxpool.Pool
	client *Client
}

func NewStore(db *pgxpool.Pool, client *Client) *Store {
	return &Store{db: db, client: client}
}

// Configured reports whether a service account is configured.
func (s *Store) Configured() bool { return s != nil && s.client.Configured() }

// Status is the outcome of the latest sync, safe to show to operators.
type Status struct {
	Configured    bool       `json:"configured"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	NodeCount     int        `json:"nodeCount"`
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	status := Status{Configured: s.Configured()}
	var lastError *string
	if err := s.db.QueryRow(ctx, `SELECT last_attempt_at, last_success_at, last_error, node_count FROM meshcentral_sync_state`).Scan(
		&status.LastAttemptAt, &status.LastSuccessAt, &lastError, &status.NodeCount); err != nil {
		return Status{}, fmt.Errorf("read meshcentral sync state: %w", err)
	}
	if lastError != nil {
		status.LastError = *lastError
	}
	return status, nil
}

// Run syncs once at start and then at every interval until ctx ends.
func (s *Store) Run(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	if !s.Configured() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
			// Client errors are sentinel-based and never carry credentials.
			logger.Warn("meshcentral sync failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sync replaces the observed node state with a fresh listing. Nodes that are
// no longer listed are marked absent; bindings are never changed here.
func (s *Store) Sync(ctx context.Context) error {
	nodes, err := s.client.Nodes(ctx)
	if err != nil {
		if _, recordErr := s.db.Exec(ctx, `UPDATE meshcentral_sync_state SET last_attempt_at = now(), last_error = $1`, err.Error()); recordErr != nil {
			return fmt.Errorf("record meshcentral sync failure: %w", recordErr)
		}
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin meshcentral sync: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
		if _, err := tx.Exec(ctx, `INSERT INTO meshcentral_nodes
			(node_id, mesh_id, name, hostname, os, agent_connected, mac_addresses, ip_addresses, last_connect_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (node_id) DO UPDATE SET mesh_id = EXCLUDED.mesh_id, name = EXCLUDED.name, hostname = EXCLUDED.hostname,
				os = EXCLUDED.os, agent_connected = EXCLUDED.agent_connected, mac_addresses = EXCLUDED.mac_addresses,
				ip_addresses = EXCLUDED.ip_addresses, last_connect_at = COALESCE(EXCLUDED.last_connect_at, meshcentral_nodes.last_connect_at),
				present = true, last_synced_at = now()`,
			node.ID, node.MeshID, node.Name, node.Hostname, node.OS, node.AgentConnected(), node.MACs, node.Addresses, node.LastConnectAt); err != nil {
			return fmt.Errorf("store meshcentral node: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE meshcentral_nodes SET present = false, agent_connected = false
		WHERE present AND NOT (node_id = ANY($1))`, ids); err != nil {
		return fmt.Errorf("mark absent meshcentral nodes: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE meshcentral_sync_state SET last_attempt_at = now(), last_success_at = now(),
		last_error = NULL, node_count = $1`, len(nodes)); err != nil {
		return fmt.Errorf("record meshcentral sync: %w", err)
	}
	return tx.Commit(ctx)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type match struct {
	nodeID, name, hostname, os string
	connected, present, bound  bool
	lastConnectAt              *time.Time
	macs                       []string
	hostnameMatch              bool
}

// matches lists nodes bound to the device and unbound present nodes whose MAC
// addresses or hostname match it. nodeID narrows the result to one node.
func matches(ctx context.Context, db querier, deviceID, nodeID string) ([]match, error) {
	rows, err := db.Query(ctx, `WITH device_macs AS (
			SELECT lower(value) AS mac FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'mac'
			UNION SELECT mac_address::text FROM network_interfaces WHERE device_id = $1::uuid AND mac_address IS NOT NULL
		), device_names AS (
			SELECT lower(value) AS name FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'hostname'
		), bound AS (
			SELECT value AS node_id FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'meshcentral'
		)
		SELECT n.node_id, n.name, n.hostname, n.os, n.agent_connected, n.present, n.last_connect_at,
			n.node_id IN (SELECT node_id FROM bound) AS bound,
			ARRAY(SELECT m FROM unnest(n.mac_addresses) m WHERE m IN (SELECT mac FROM device_macs) ORDER BY m) AS macs,
			n.hostname <> '' AND EXISTS (SELECT 1 FROM device_names d WHERE d.name = n.hostname OR split_part(d.name, '.', 1) = split_part(n.hostname, '.', 1)) AS hostname_match
		FROM meshcentral_nodes n
		WHERE ($2 = '' OR n.node_id = $2) AND (n.node_id IN (SELECT node_id FROM bound) OR (n.present
			AND NOT EXISTS (SELECT 1 FROM device_identities b WHERE b.identity_type = 'meshcentral' AND b.value = n.node_id)
			AND (n.mac_addresses && ARRAY(SELECT mac FROM device_macs)
				OR (n.hostname <> '' AND EXISTS (SELECT 1 FROM device_names d WHERE d.name = n.hostname OR split_part(d.name, '.', 1) = split_part(n.hostname, '.', 1))))))
		ORDER BY bound DESC, n.node_id
		LIMIT 20`, deviceID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("find meshcentral nodes for device: %w", err)
	}
	defer rows.Close()
	items := []match{}
	for rows.Next() {
		var item match
		if err := rows.Scan(&item.nodeID, &item.name, &item.hostname, &item.os, &item.connected, &item.present, &item.lastConnectAt,
			&item.bound, &item.macs, &item.hostnameMatch); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m match) evidence() []remote.EvidenceSignal {
	signals := []remote.EvidenceSignal{}
	for _, mac := range m.macs {
		signals = append(signals, remote.EvidenceSignal{Type: "mac", Value: mac})
	}
	if m.hostnameMatch {
		signals = append(signals, remote.EvidenceSignal{Type: "hostname", Value: m.hostname})
	}
	return signals
}

// Association states reported on MeshCentral endpoints.
const (
	AssociationConfirmed = "confirmed"
	// AssociationCandidate has MAC evidence and may be confirmed.
	AssociationCandidate = "candidate"
	// AssociationWeak matches on hostname only and cannot be confirmed.
	AssociationWeak = "weak"
)

// Interactive capabilities MeshCentral offers. They stay non-executable until
// audited sessions exist (#106).
var nodeCapabilities = []string{agentproto.CapabilityRemoteDesktop, agentproto.CapabilityRemoteTerminal, agentproto.CapabilityFileTransfer}

// Provider exposes bound and candidate MeshCentral nodes for a device.
type Provider struct {
	Store *Store
}

func (p Provider) Name() string { return ProviderName }

func (p Provider) EndpointsForDevice(ctx context.Context, deviceID string) ([]remote.Endpoint, error) {
	items, err := matches(ctx, p.Store.db, deviceID, "")
	if err != nil {
		return nil, err
	}
	endpoints := make([]remote.Endpoint, 0, len(items))
	for _, item := range items {
		association := AssociationWeak
		switch {
		case item.bound:
			association = AssociationConfirmed
		case len(item.macs) > 0:
			association = AssociationCandidate
		}
		status := "present"
		if !item.present {
			status = "absent"
		}
		name := item.name
		if name == "" {
			name = item.nodeID
		}
		endpoints = append(endpoints, remote.Endpoint{
			Provider: ProviderName, EndpointID: item.nodeID, DisplayName: name, Status: status, Online: item.present && item.connected,
			Version: item.os, LastSeenAt: item.lastConnectAt, Association: association,
			Capabilities: remote.Capabilities(nodeCapabilities), Evidence: item.evidence(),
		})
	}
	return endpoints, nil
}

// Actor identifies the operator behind a binding change.
type Actor struct {
	UserID        string
	SourceAddress string
}

// Confirm binds a node to a device. The node must be present and unbound, and
// at least one of its MAC addresses must match the device; hostname evidence
// alone is refused. Nodes are never bound automatically.
func (s *Store) Confirm(ctx context.Context, deviceID, nodeID string, actor Actor) error {
	if !ValidNodeID(nodeID) {
		return ErrNodeNotFound
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin meshcentral binding: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Lock the node so concurrent confirmations cannot bind it twice.
	var present bool
	err = tx.QueryRow(ctx, `SELECT present FROM meshcentral_nodes WHERE node_id = $1 FOR UPDATE`, nodeID).Scan(&present)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !present) {
		return s.refuse(ctx, actor, deviceID, nodeID, "node_not_found", ErrNodeNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock meshcentral node: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT 1 FROM devices WHERE entity_id = $1::uuid FOR UPDATE`, deviceID).Scan(new(int)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeviceNotFound
		}
		return fmt.Errorf("lock device: %w", err)
	}
	var boundTo string
	err = tx.QueryRow(ctx, `SELECT device_id::text FROM device_identities WHERE identity_type = 'meshcentral' AND value = $1 LIMIT 1`, nodeID).Scan(&boundTo)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check meshcentral binding: %w", err)
	}
	if boundTo != "" {
		return s.refuse(ctx, actor, deviceID, nodeID, "already_bound", ErrAlreadyBound)
	}
	items, err := matches(ctx, tx, deviceID, nodeID)
	if err != nil {
		return err
	}
	if len(items) != 1 || len(items[0].macs) == 0 {
		return s.refuse(ctx, actor, deviceID, nodeID, "weak_evidence", ErrWeakEvidence)
	}
	item := items[0]
	if _, err := tx.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence, manually_confirmed_at)
		VALUES ($1::uuid, 'meshcentral', $2, 'meshcentral', 100, now())`, deviceID, nodeID); err != nil {
		return fmt.Errorf("store meshcentral binding: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "meshcentral.binding.confirmed", TargetType: "device", TargetID: deviceID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"nodeId": nodeID, "matchedMacs": item.macs, "hostnameMatch": item.hostnameMatch}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Unbind removes a confirmed binding, for example after a reinstall left it
// pointing at an absent node.
func (s *Store) Unbind(ctx context.Context, deviceID, nodeID string, actor Actor) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin meshcentral unbind: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `DELETE FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'meshcentral' AND value = $2`, deviceID, nodeID)
	if err != nil {
		return fmt.Errorf("remove meshcentral binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotBound
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "meshcentral.binding.removed", TargetType: "device", TargetID: deviceID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"nodeId": nodeID}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// refuse audits a refused binding outside the rolled-back transaction.
func (s *Store) refuse(ctx context.Context, actor Actor, deviceID, nodeID, code string, cause error) error {
	if err := audit.Write(ctx, s.db, audit.Entry{ActorUserID: actor.UserID, Action: "meshcentral.binding.confirmed", TargetType: "device", TargetID: deviceID,
		SourceAddress: actor.SourceAddress, Result: audit.ResultDenied, ErrorCode: code, Parameters: map[string]any{"nodeId": nodeID}}); err != nil {
		return err
	}
	return cause
}
