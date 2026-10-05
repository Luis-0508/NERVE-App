package devices

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nerve-labs/nerve/internal/audit"
)

var ErrAgentDecisionRequired = errors.New("the device has a management agent; choose keep, move or detach")

// Agent handling during a split. A split never guesses which half the
// management agent belongs to.
const (
	SplitAgentKeep   = "keep"
	SplitAgentMove   = "move"
	SplitAgentDetach = "detach"
)

// SplitRequest moves explicitly chosen evidence into a new device. Moving an
// interface also moves its addresses, discovery bindings and MAC evidence.
type SplitRequest struct {
	DisplayName  string
	InterfaceIDs []string
	IdentityIDs  []string
	Agent        string
	Reason       string
}

type SplitResult struct {
	SplitID     string         `json:"splitId"`
	NewDeviceID string         `json:"newDeviceId"`
	Summary     map[string]any `json:"summary"`
}

func validUUIDs(values []string) bool {
	for _, value := range values {
		if len(value) != 36 || strings.Count(value, "-") != 4 {
			return false
		}
	}
	return true
}

// Split separates records that NERVE grouped into one device although they
// belong to two. Observations and events stay with the original device:
// they cannot be attributed reliably after the fact.
func (s *Service) Split(ctx context.Context, sourceID string, request SplitRequest, actor Actor) (SplitResult, error) {
	if len(request.InterfaceIDs) == 0 && len(request.IdentityIDs) == 0 {
		return SplitResult{}, fmt.Errorf("%w: choose at least one interface or identity to move", ErrInvalidInput)
	}
	if !validUUIDs(request.InterfaceIDs) || !validUUIDs(request.IdentityIDs) {
		return SplitResult{}, fmt.Errorf("%w: interface and identity IDs must be UUIDs", ErrInvalidInput)
	}
	name, err := cleanText(&request.DisplayName, 200, "display name")
	if err != nil {
		return SplitResult{}, err
	}
	reason, err := reasonText(request.Reason)
	if err != nil {
		return SplitResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return SplitResult{}, fmt.Errorf("begin split: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockDevice(ctx, tx, sourceID); err != nil {
		return SplitResult{}, err
	}

	var foreignInterfaces, foreignIdentities int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM unnest($2::uuid[]) id WHERE NOT EXISTS (SELECT 1 FROM network_interfaces WHERE entity_id = id AND device_id = $1::uuid)),
		(SELECT count(*) FROM unnest($3::uuid[]) id WHERE NOT EXISTS (SELECT 1 FROM device_identities WHERE device_identities.id = id AND device_id = $1::uuid AND identity_type <> 'agent'))`,
		sourceID, request.InterfaceIDs, request.IdentityIDs).Scan(&foreignInterfaces, &foreignIdentities); err != nil {
		return SplitResult{}, fmt.Errorf("check split selection: %w", err)
	}
	if foreignInterfaces > 0 || foreignIdentities > 0 {
		return SplitResult{}, fmt.Errorf("%w: every selected interface and identity must belong to this device (agent identities move with the agent)", ErrInvalidInput)
	}
	var agentID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT id::text FROM agents WHERE device_id = $1::uuid AND status <> 'removed'), '')`, sourceID).Scan(&agentID); err != nil {
		return SplitResult{}, fmt.Errorf("check device agent: %w", err)
	}
	switch {
	case agentID == "" && (request.Agent == "" || request.Agent == SplitAgentKeep):
		request.Agent = ""
	case agentID == "":
		return SplitResult{}, fmt.Errorf("%w: the device has no management agent", ErrInvalidInput)
	case request.Agent != SplitAgentKeep && request.Agent != SplitAgentMove && request.Agent != SplitAgentDetach:
		return SplitResult{}, ErrAgentDecisionRequired
	}

	newName := *name
	if newName == "" {
		_ = tx.QueryRow(ctx, `SELECT COALESCE(
			(SELECT host(ip.address) FROM network_interfaces ni JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
				JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id WHERE ni.entity_id = ANY($1::uuid[]) ORDER BY ip.valid_until NULLS FIRST LIMIT 1),
			(SELECT mac_address::text FROM network_interfaces WHERE entity_id = ANY($1::uuid[]) AND mac_address IS NOT NULL LIMIT 1),
			'Split device')`, request.InterfaceIDs).Scan(&newName)
	}
	var newID string
	if err := tx.QueryRow(ctx, `INSERT INTO entities (kind, name, status, metadata) SELECT 'device', $1, e.status, '{"createdBy":"split"}'::jsonb
		FROM entities e WHERE e.id = $2::uuid RETURNING id::text`, newName, sourceID).Scan(&newID); err != nil {
		return SplitResult{}, fmt.Errorf("create split device: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO devices (entity_id, classification, discovered_name, display_name, metadata_updated_at, metadata_updated_by)
		VALUES ($1::uuid, 'unknown', $2, NULLIF($3, ''), CASE WHEN $3 <> '' THEN clock_timestamp() END, CASE WHEN $3 <> '' THEN $4::uuid END)`,
		newID, newName, *name, actor.UserID); err != nil {
		return SplitResult{}, fmt.Errorf("create split device record: %w", err)
	}

	moved := map[string]int64{}
	run := func(key, sql string, args ...any) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("split %s: %w", key, err)
		}
		moved[key] += tag.RowsAffected()
		return nil
	}
	if err := run("interfaces", `UPDATE network_interfaces SET device_id = $2::uuid WHERE device_id = $1::uuid AND entity_id = ANY($3::uuid[])`,
		sourceID, newID, request.InterfaceIDs); err != nil {
		return SplitResult{}, err
	}
	if err := run("discoveryBindings", `UPDATE discovery_bindings SET device_id = $2::uuid WHERE device_id = $1::uuid AND interface_id = ANY($3::uuid[])`,
		sourceID, newID, request.InterfaceIDs); err != nil {
		return SplitResult{}, err
	}
	if err := run("identities", `UPDATE device_identities SET device_id = $2::uuid WHERE device_id = $1::uuid AND identity_type <> 'agent' AND (id = ANY($4::uuid[])
		OR (identity_type = 'mac' AND lower(value) IN (SELECT mac_address::text FROM network_interfaces WHERE entity_id = ANY($3::uuid[]) AND mac_address IS NOT NULL)))`,
		sourceID, newID, request.InterfaceIDs, request.IdentityIDs); err != nil {
		return SplitResult{}, err
	}

	switch request.Agent {
	case SplitAgentMove:
		if err := run("agent", `UPDATE agents SET device_id = $2::uuid, association_status = 'confirmed', associated_at = now(),
			associated_by = $3::uuid, updated_at = now() WHERE id = $1::uuid`, agentID, newID, actor.UserID); err != nil {
			return SplitResult{}, err
		}
		if err := run("agentEvidence", `UPDATE device_identities SET device_id = $2::uuid WHERE device_id = $1::uuid AND (source = $3 OR identity_type = 'agent')`,
			sourceID, newID, SourceAgent); err != nil {
			return SplitResult{}, err
		}
		if err := run("agentFacts", `UPDATE device_attributes SET device_id = $2::uuid WHERE device_id = $1::uuid AND source = $3`, sourceID, newID, SourceAgent); err != nil {
			return SplitResult{}, err
		}
		// Recompute the original device without the facts that just left.
		if err := ForgetSource(ctx, tx, sourceID, SourceAgent); err != nil {
			return SplitResult{}, err
		}
	case SplitAgentDetach:
		if err := run("agent", `UPDATE agents SET device_id = NULL, association_status = 'unassociated', association_evidence = '[]'::jsonb,
			associated_at = NULL, associated_by = NULL, updated_at = now() WHERE id = $1::uuid`, agentID); err != nil {
			return SplitResult{}, err
		}
		if err := run("agentEvidence", `DELETE FROM device_identities WHERE identity_type = 'agent' AND value = $1`, agentID); err != nil {
			return SplitResult{}, err
		}
		if err := ForgetSource(ctx, tx, sourceID, SourceAgent); err != nil {
			return SplitResult{}, err
		}
	}
	for _, id := range []string{sourceID, newID} {
		if err := RefreshDiscovered(ctx, tx, id); err != nil {
			return SplitResult{}, err
		}
	}

	summary := map[string]any{"moved": moved, "agent": request.Agent}
	var splitID string
	if err := tx.QueryRow(ctx, `INSERT INTO device_splits (source_device_id, new_device_id, split_by, reason, summary)
		VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, ''), $5) RETURNING id::text`, sourceID, newID, actor.UserID, reason, summary).Scan(&splitID); err != nil {
		return SplitResult{}, fmt.Errorf("record split: %w", err)
	}
	params := map[string]any{"splitId": splitID, "newDeviceId": newID, "moved": moved, "interfaceIds": request.InterfaceIDs,
		"identityIds": request.IdentityIDs, "agent": request.Agent}
	if agentID != "" {
		params["agentId"] = agentID
	}
	if reason != "" {
		params["reason"] = reason
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "device.split", TargetType: "device", TargetID: sourceID,
		SourceAddress: actor.SourceAddress, Parameters: params}); err != nil {
		return SplitResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details) VALUES
		($1::uuid, 'device.split', 'info', 'identity', 'Part of this device was split into a new device.', jsonb_build_object('newDeviceId', $2::text)),
		($2::uuid, 'device.split', 'info', 'identity', 'This device was split from another device record.', jsonb_build_object('sourceDeviceId', $1::text))`,
		sourceID, newID); err != nil {
		return SplitResult{}, fmt.Errorf("record split events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return SplitResult{}, fmt.Errorf("commit split: %w", err)
	}
	return SplitResult{SplitID: splitID, NewDeviceID: newID, Summary: summary}, nil
}
