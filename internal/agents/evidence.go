package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/devices"
)

// Confidence of identity evidence reported by an enrolled agent. The agent's
// own identity is cryptographic; what it reports about its host is
// authenticated but self-described.
const (
	confidenceAgentMachine  = 95
	confidenceAgentMAC      = 90
	confidenceAgentHostname = 80
	confidenceAgentIP       = 40
)

// syncAgentEvidence copies an associated agent's host facts into the device's
// identity evidence and provenance, so discovery and remote management
// converge on one device record instead of keeping parallel identities.
func syncAgentEvidence(ctx context.Context, tx pgx.Tx, agentID string) error {
	var (
		deviceID, hostname, osName, osVersion, architecture, machine string
		macs, addresses                                              []string
	)
	err := tx.QueryRow(ctx, `SELECT COALESCE(device_id::text, ''), COALESCE(hostname, ''), COALESCE(os, ''), COALESCE(os_version, ''),
		COALESCE(architecture, ''), COALESCE(machine_fingerprint, ''), reported_mac_addresses::text[],
		ARRAY(SELECT host(x) FROM unnest(reported_ip_addresses) x)
		FROM agents WHERE id = $1::uuid AND status <> 'removed'`, agentID).Scan(
		&deviceID, &hostname, &osName, &osVersion, &architecture, &machine, &macs, &addresses)
	if err != nil || deviceID == "" {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	// Replace this agent's previous claims so stale values do not linger.
	if _, err := tx.Exec(ctx, `DELETE FROM device_identities WHERE device_id = $1::uuid AND source = $2 AND identity_type <> 'agent'`,
		deviceID, devices.SourceAgent); err != nil {
		return fmt.Errorf("clear agent evidence: %w", err)
	}
	add := func(identityType, value string, confidence int) error {
		if value == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence)
			VALUES ($1::uuid, $2, $3, $4, $5)
			ON CONFLICT (identity_type, value, device_id) DO UPDATE SET last_seen_at = now(),
				confidence = GREATEST(device_identities.confidence, EXCLUDED.confidence)`,
			deviceID, identityType, value, devices.SourceAgent, confidence)
		if err != nil {
			return fmt.Errorf("record agent %s evidence: %w", identityType, err)
		}
		return nil
	}
	if err := add("machine", machine, confidenceAgentMachine); err != nil {
		return err
	}
	if err := add("hostname", strings.ToLower(hostname), confidenceAgentHostname); err != nil {
		return err
	}
	for _, mac := range macs {
		if err := add("mac", mac, confidenceAgentMAC); err != nil {
			return err
		}
	}
	for _, address := range addresses {
		if err := add("ip", address, confidenceAgentIP); err != nil {
			return err
		}
	}
	if _, err := devices.RecordVendor(ctx, tx, deviceID); err != nil {
		return err
	}
	operatingSystem := strings.TrimSpace(osVersion)
	if operatingSystem == "" {
		operatingSystem = osName
	}
	for attribute, value := range map[string]string{"hostname": hostname, "operating_system": operatingSystem, "architecture": architecture} {
		if err := devices.RecordAttribute(ctx, tx, deviceID, attribute, devices.SourceAgent, value, devices.ConfidenceAgent); err != nil {
			return err
		}
	}
	return nil
}
