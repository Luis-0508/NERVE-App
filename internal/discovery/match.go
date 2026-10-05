package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Match reasons recorded with each observation, so the identity evidence can
// explain why an observation was attributed to a device.
const (
	matchMAC           = "mac"
	matchMACAmbiguous  = "mac-ambiguous"
	matchBinding       = "address-binding"
	matchAgentAddress  = "agent-reported-address"
	matchNewDevice     = "new-device"
	matchAddressReused = "address-reused-by-other-hardware"
)

// resolveDevice decides which existing device an observation belongs to. It
// never merges devices; ambiguity is resolved deterministically and left
// visible for duplicate detection instead of being hidden.
//
//  1. A MAC identity is strong evidence. If it belongs to several devices, the
//     one with a live management agent wins, then the most recently seen.
//  2. Without a MAC match, the device previously bound to this address is
//     used, unless the observation carries a MAC and that device already has a
//     different MAC: then the address was reused by other hardware.
//  3. Otherwise an address that an enrolled agent currently reports for its
//     associated device links the observation to that device, so an IP change
//     does not create a duplicate when the MAC is not visible.
//  4. Otherwise the caller creates a new device.
func resolveDevice(ctx context.Context, tx pgx.Tx, targetID, address, mac string) (deviceID, reason string, err error) {
	if mac != "" {
		rows, err := tx.Query(ctx, `SELECT DISTINCT d.entity_id::text,
				EXISTS (SELECT 1 FROM agents a WHERE a.device_id = d.entity_id AND a.status <> 'removed'),
				e.last_seen_at
			FROM device_identities i
			JOIN devices d ON d.entity_id = i.device_id AND d.merged_into IS NULL
			JOIN entities e ON e.id = d.entity_id
			WHERE i.identity_type = 'mac' AND lower(i.value) = $1
			ORDER BY 2 DESC, 3 DESC, 1`, mac)
		if err != nil {
			return "", "", fmt.Errorf("resolve device MAC identity: %w", err)
		}
		var candidates []string
		for rows.Next() {
			var id string
			var hasAgent bool
			var seen any
			if err := rows.Scan(&id, &hasAgent, &seen); err != nil {
				rows.Close()
				return "", "", err
			}
			candidates = append(candidates, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", "", err
		}
		switch len(candidates) {
		case 0:
		case 1:
			return candidates[0], matchMAC, nil
		default:
			return candidates[0], matchMACAmbiguous, nil
		}
	}

	var boundDevice string
	err = tx.QueryRow(ctx, `SELECT COALESCE(d.merged_into, d.entity_id)::text FROM discovery_bindings b
		JOIN devices d ON d.entity_id = b.device_id WHERE b.target_id = $1::uuid AND b.address = $2::inet`, targetID, address).Scan(&boundDevice)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("resolve discovery address binding: %w", err)
	}
	if boundDevice != "" {
		if mac == "" {
			return boundDevice, matchBinding, nil
		}
		var hasOtherMAC bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'mac')`,
			boundDevice).Scan(&hasOtherMAC); err != nil {
			return "", "", fmt.Errorf("check bound device MAC identity: %w", err)
		}
		if !hasOtherMAC {
			return boundDevice, matchBinding, nil
		}
		reason = matchAddressReused
	}

	rows, err := tx.Query(ctx, `SELECT a.device_id::text FROM agents a JOIN devices d ON d.entity_id = a.device_id AND d.merged_into IS NULL
		WHERE a.status = 'active' AND $1::inet = ANY(a.reported_ip_addresses) LIMIT 2`, address)
	if err != nil {
		return "", "", fmt.Errorf("resolve agent-reported address: %w", err)
	}
	var agentDevices []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", "", err
		}
		agentDevices = append(agentDevices, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	// An agent-reported address only counts when it is unambiguous and the
	// observation does not carry a MAC the agent did not report.
	if len(agentDevices) == 1 && mac == "" {
		return agentDevices[0], matchAgentAddress, nil
	}
	if reason == "" {
		reason = matchNewDevice
	}
	return "", reason, nil
}
