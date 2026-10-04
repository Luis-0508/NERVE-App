package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/audit"
)

// Heartbeat records liveness, refreshes host facts and capabilities, and
// re-evaluates the device association when facts change or it is unresolved.
func (r *Repository) Heartbeat(ctx context.Context, principal Principal, request agentproto.HeartbeatRequest) error {
	if err := request.Host.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if len(request.Capabilities) > agentproto.MaxCapabilities {
		return fmt.Errorf("%w: too many capabilities", ErrInvalidInput)
	}
	facts := normalizeFacts(request.Host)
	capabilities := agentproto.FilterCapabilities(request.Capabilities)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin heartbeat: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var previousDigest, associationStatus string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(facts_digest, ''), association_status FROM agents WHERE id = $1::uuid AND status = 'active' FOR UPDATE`, principal.AgentID).Scan(&previousDigest, &associationStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCredentialRevoked
		}
		return fmt.Errorf("lock agent heartbeat: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET capabilities = $2, last_error = NULLIF($3, ''), last_heartbeat_at = now(),
		last_seen_at = now(), updated_at = now() WHERE id = $1::uuid`, principal.AgentID, capabilities,
		truncate(request.LastError, agentproto.MaxLastErrorLength)); err != nil {
		return fmt.Errorf("record heartbeat: %w", err)
	}
	factsChanged := factsDigest(facts) != previousDigest
	if factsChanged {
		if err := storeFacts(ctx, tx, principal.AgentID, facts); err != nil {
			return err
		}
	}
	// Unresolved agents are re-evaluated on every heartbeat so a device that
	// discovery finds after enrollment is still linked; resolved ones only when
	// their reported facts change.
	if factsChanged || associationStatus == "unassociated" || associationStatus == "candidate" {
		if err := evaluateAssociation(ctx, tx, principal.AgentID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit heartbeat: %w", err)
	}
	return nil
}

type candidate struct {
	evidence Evidence
	mac      bool
}

// evaluateAssociation proposes a device for an agent from reported evidence.
//
//   - A MAC address that matches exactly one device is strong evidence and
//     associates automatically, unless that device already has a live agent.
//   - Hostname and IP matches are weak: they are kept as candidates for an
//     operator to confirm and never associate on their own.
//   - Operator decisions (confirmed, dismissed) are never changed here.
//
// Devices are never created or merged.
func evaluateAssociation(ctx context.Context, tx pgx.Tx, agentID string) error {
	var (
		status, currentDevice string
		hostname              string
		macs, addresses       []string
	)
	err := tx.QueryRow(ctx, `SELECT association_status, COALESCE(device_id::text, ''), COALESCE(hostname, ''),
		reported_mac_addresses::text[], ARRAY(SELECT host(x) FROM unnest(reported_ip_addresses) x)
		FROM agents WHERE id = $1::uuid AND status <> 'removed'`, agentID).Scan(&status, &currentDevice, &hostname, &macs, &addresses)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read agent evidence: %w", err)
	}
	if status == "confirmed" || status == "dismissed" {
		return nil
	}

	candidates := map[string]*candidate{}
	add := func(deviceID, deviceName, signalType, value string) {
		item, ok := candidates[deviceID]
		if !ok {
			item = &candidate{evidence: Evidence{DeviceID: deviceID, DeviceName: deviceName, Signals: []Signal{}}}
			candidates[deviceID] = item
		}
		for _, existing := range item.evidence.Signals {
			if existing.Type == signalType && existing.Value == value {
				return
			}
		}
		item.evidence.Signals = append(item.evidence.Signals, Signal{Type: signalType, Value: value})
		if signalType == "mac" {
			item.mac = true
		}
	}
	collect := func(query string, signalType string, args ...any) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("find %s association candidates: %w", signalType, err)
		}
		defer rows.Close()
		for rows.Next() {
			var deviceID, deviceName, value string
			if err := rows.Scan(&deviceID, &deviceName, &value); err != nil {
				return err
			}
			add(deviceID, deviceName, signalType, value)
		}
		return rows.Err()
	}
	if len(macs) > 0 {
		if err := collect(`SELECT DISTINCT d.entity_id::text, e.name, lower(i.value)
			FROM device_identities i JOIN devices d ON d.entity_id = i.device_id JOIN entities e ON e.id = d.entity_id
			WHERE i.identity_type = 'mac' AND lower(i.value) = ANY($1)
			UNION
			SELECT DISTINCT n.device_id::text, e.name, n.mac_address::text
			FROM network_interfaces n JOIN entities e ON e.id = n.device_id WHERE n.mac_address::text = ANY($1)`, "mac", macs); err != nil {
			return err
		}
	}
	if hostname != "" {
		names := []string{strings.ToLower(hostname)}
		if short, _, found := strings.Cut(names[0], "."); found && short != "" {
			names = append(names, short)
		}
		if err := collect(`SELECT DISTINCT d.entity_id::text, e.name, i.value
			FROM device_identities i JOIN devices d ON d.entity_id = i.device_id JOIN entities e ON e.id = d.entity_id
			WHERE i.identity_type = 'hostname' AND (i.value = ANY($1) OR split_part(i.value, '.', 1) = ANY($1))`, "hostname", names); err != nil {
			return err
		}
	}
	if len(addresses) > 0 {
		if err := collect(`SELECT DISTINCT n.device_id::text, e.name, host(ip.address)
			FROM ip_addresses ip JOIN interface_addresses ia ON ia.ip_address_id = ip.entity_id
			JOIN network_interfaces n ON n.entity_id = ia.interface_id JOIN entities e ON e.id = n.device_id
			WHERE ip.valid_until IS NULL AND host(ip.address) = ANY($1)`, "ip", addresses); err != nil {
			return err
		}
	}

	ordered := make([]*candidate, 0, len(candidates))
	macDevices := 0
	for _, item := range candidates {
		if item.mac {
			macDevices++
			item.evidence.Strength = "strong"
		} else {
			item.evidence.Strength = "weak"
		}
		sort.Slice(item.evidence.Signals, func(i, j int) bool {
			if item.evidence.Signals[i].Type != item.evidence.Signals[j].Type {
				return item.evidence.Signals[i].Type < item.evidence.Signals[j].Type
			}
			return item.evidence.Signals[i].Value < item.evidence.Signals[j].Value
		})
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].mac != ordered[j].mac {
			return ordered[i].mac
		}
		if len(ordered[i].evidence.Signals) != len(ordered[j].evidence.Signals) {
			return len(ordered[i].evidence.Signals) > len(ordered[j].evidence.Signals)
		}
		return ordered[i].evidence.DeviceID < ordered[j].evidence.DeviceID
	})
	if len(ordered) > 5 {
		ordered = ordered[:5]
	}
	evidence := make([]Evidence, 0, len(ordered))
	for _, item := range ordered {
		evidence = append(evidence, item.evidence)
	}

	nextStatus, nextDevice := "unassociated", ""
	switch {
	case macDevices == 1 && ordered[0].mac:
		deviceID := ordered[0].evidence.DeviceID
		var conflicting string
		err := tx.QueryRow(ctx, `SELECT id::text FROM agents WHERE device_id = $1::uuid AND status <> 'removed' AND id <> $2::uuid`, deviceID, agentID).Scan(&conflicting)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check device agent conflict: %w", err)
		}
		if conflicting == "" {
			nextStatus, nextDevice = "automatic", deviceID
		} else {
			evidence[0].Note = "The device already has another live agent (" + conflicting + "). An operator must decide."
			nextStatus = "candidate"
		}
	case macDevices > 1:
		for index := range evidence {
			if evidence[index].Strength == "strong" {
				evidence[index].Note = "MAC evidence matches several devices; an operator must decide."
			}
		}
		nextStatus = "candidate"
	case len(evidence) > 0:
		nextStatus = "candidate"
	}
	if nextStatus == status && nextDevice == currentDevice {
		encoded, _ := json.Marshal(evidence)
		_, err := tx.Exec(ctx, `UPDATE agents SET association_evidence = $2::jsonb WHERE id = $1::uuid AND association_evidence IS DISTINCT FROM $2::jsonb`, agentID, string(encoded))
		return err
	}
	if err := setAssociation(ctx, tx, agentID, nextDevice, nextStatus, evidence, ""); err != nil {
		return err
	}
	return audit.Write(ctx, tx, audit.Entry{ActorAgentID: agentID, Action: "agent.association.evaluated", TargetType: "agent", TargetID: agentID,
		Parameters: map[string]any{"previousStatus": status, "status": nextStatus, "previousDeviceId": currentDevice, "deviceId": nextDevice, "candidates": len(evidence)}})
}

// AssociationAction is an operator decision about an agent's device.
type AssociationAction struct {
	Action   string // confirm, dismiss, reevaluate
	DeviceID string
}

// UpdateAssociation applies an operator decision. Confirming binds the agent
// to a device; dismissing records that it belongs to no known device;
// re-evaluating hands the decision back to automatic evidence matching.
func (r *Repository) UpdateAssociation(ctx context.Context, agentID string, action AssociationAction, actor Actor) (Agent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Agent{}, fmt.Errorf("begin association change: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status, previousStatus, previousDevice string
	var evidenceRaw []byte
	err = tx.QueryRow(ctx, `SELECT status, association_status, COALESCE(device_id::text, ''), association_evidence FROM agents WHERE id = $1::uuid FOR UPDATE`, agentID).Scan(
		&status, &previousStatus, &previousDevice, &evidenceRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("lock agent association: %w", err)
	}
	if status == "removed" {
		return Agent{}, ErrAgentRemoved
	}
	var evidence []Evidence
	_ = json.Unmarshal(evidenceRaw, &evidence)

	switch action.Action {
	case "confirm":
		if !isUUID(action.DeviceID) {
			return Agent{}, fmt.Errorf("%w: a device ID is required", ErrInvalidInput)
		}
		var deviceName string
		err := tx.QueryRow(ctx, `SELECT e.name FROM devices d JOIN entities e ON e.id = d.entity_id WHERE d.entity_id = $1::uuid FOR UPDATE OF d`, action.DeviceID).Scan(&deviceName)
		if errors.Is(err, pgx.ErrNoRows) {
			return Agent{}, ErrDeviceNotFound
		}
		if err != nil {
			return Agent{}, fmt.Errorf("lock association device: %w", err)
		}
		var conflicting string
		err = tx.QueryRow(ctx, `SELECT id::text FROM agents WHERE device_id = $1::uuid AND status <> 'removed' AND id <> $2::uuid`, action.DeviceID, agentID).Scan(&conflicting)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Agent{}, fmt.Errorf("check device agent conflict: %w", err)
		}
		if conflicting != "" {
			return Agent{}, ErrDeviceHasAgent
		}
		kept := []Evidence{}
		for _, item := range evidence {
			if item.DeviceID == action.DeviceID {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			kept = append(kept, Evidence{DeviceID: action.DeviceID, DeviceName: deviceName, Strength: "operator", Signals: []Signal{{Type: "operator", Value: "confirmed"}}})
		}
		if err := setAssociation(ctx, tx, agentID, action.DeviceID, "confirmed", kept, actor.UserID); err != nil {
			return Agent{}, err
		}
	case "dismiss":
		if err := setAssociation(ctx, tx, agentID, "", "dismissed", evidence, actor.UserID); err != nil {
			return Agent{}, err
		}
	case "reevaluate":
		if err := setAssociation(ctx, tx, agentID, "", "unassociated", nil, ""); err != nil {
			return Agent{}, err
		}
		if err := evaluateAssociation(ctx, tx, agentID); err != nil {
			return Agent{}, err
		}
	default:
		return Agent{}, fmt.Errorf("%w: action must be confirm, dismiss or reevaluate", ErrInvalidInput)
	}
	var nextStatus, nextDevice string
	if err := tx.QueryRow(ctx, `SELECT association_status, COALESCE(device_id::text, '') FROM agents WHERE id = $1::uuid`, agentID).Scan(&nextStatus, &nextDevice); err != nil {
		return Agent{}, err
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.association.changed", TargetType: "agent", TargetID: agentID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"action": action.Action, "previousStatus": previousStatus,
			"previousDeviceId": previousDevice, "status": nextStatus, "deviceId": nextDevice}}); err != nil {
		return Agent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Agent{}, fmt.Errorf("commit association change: %w", err)
	}
	return r.Get(ctx, agentID)
}
