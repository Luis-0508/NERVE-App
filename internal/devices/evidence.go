package devices

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Evidence strength. An enrolled agent's cryptographic identity, an agent's
// machine fingerprint or a serial number identify hardware; a globally unique
// MAC nearly does; hostnames and IP addresses are reused and only hint.
const (
	StrengthStrong   = "strong"
	StrengthModerate = "moderate"
	StrengthWeak     = "weak"
	StrengthNone     = "none"
)

type DeviceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type EvidenceItem struct {
	Type              string      `json:"type"`
	Value             string      `json:"value"`
	Source            string      `json:"source"`
	Confidence        int         `json:"confidence"`
	Strength          string      `json:"strength"`
	Note              string      `json:"note,omitempty"`
	FirstSeenAt       time.Time   `json:"firstSeenAt"`
	LastSeenAt        time.Time   `json:"lastSeenAt"`
	ManuallyConfirmed bool        `json:"manuallyConfirmed"`
	SharedWith        []DeviceRef `json:"sharedWith"`
}

type AgentRef struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	Status      string     `json:"status"`
	Online      bool       `json:"online"`
	Association string     `json:"association"`
	LastSeenAt  *time.Time `json:"lastSeenAt,omitempty"`
}

type MatchCount struct {
	Reason string    `json:"reason"`
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
}

// IdentityReport explains why NERVE believes a record is one device.
type IdentityReport struct {
	DeviceID   string         `json:"deviceId"`
	MergedInto string         `json:"mergedInto,omitempty"`
	Strength   string         `json:"strength"`
	Summary    string         `json:"summary"`
	Evidence   []EvidenceItem `json:"evidence"`
	Agents     []AgentRef     `json:"agents"`
	Facts      []Attribute    `json:"facts"`
	Matches    []MatchCount   `json:"discoveryMatches"`
}

// locallyAdministered reports MACs with the U/L bit set; phones and laptops
// use such randomized addresses, so they do not identify hardware.
func locallyAdministered(mac string) bool {
	hardware, err := net.ParseMAC(mac)
	return err == nil && len(hardware) > 0 && hardware[0]&0x02 != 0
}

func classify(item *EvidenceItem) {
	switch item.Type {
	case "agent":
		item.Strength, item.Note = StrengthStrong, "Enrolled management agent with its own Ed25519 key"
	case "machine":
		item.Strength, item.Note = StrengthStrong, "Machine ID digest reported by the enrolled agent"
	case "serial":
		item.Strength = StrengthStrong
	case "mac":
		item.Strength = StrengthStrong
		if locallyAdministered(item.Value) {
			item.Strength, item.Note = StrengthModerate, "Locally administered MAC; may be randomized"
		}
	case "hostname":
		item.Strength, item.Note = StrengthWeak, "Hostnames are reused and renamed"
	case "ip":
		item.Strength, item.Note = StrengthWeak, "Addresses change and are reassigned"
	default:
		item.Strength = StrengthModerate
	}
	if len(item.SharedWith) > 0 && item.Strength == StrengthStrong {
		item.Strength = StrengthModerate
		item.Note = "Also claimed by another device; review the duplicate candidates"
	}
}

// Identity builds the identity report for one device.
func (s *Service) Identity(ctx context.Context, deviceID string) (IdentityReport, error) {
	return Identity(ctx, s.db, deviceID)
}

func Identity(ctx context.Context, db *pgxpool.Pool, deviceID string) (IdentityReport, error) {
	report := IdentityReport{DeviceID: deviceID, Evidence: []EvidenceItem{}, Agents: []AgentRef{}, Matches: []MatchCount{}}
	var merged *string
	err := db.QueryRow(ctx, `SELECT merged_into::text FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(&merged)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityReport{}, ErrNotFound
	}
	if err != nil {
		return IdentityReport{}, fmt.Errorf("read device: %w", err)
	}
	if merged != nil {
		report.MergedInto = *merged
	}

	rows, err := db.Query(ctx, `SELECT i.identity_type, i.value, i.source, i.confidence, i.first_seen_at, i.last_seen_at,
			i.manually_confirmed_at IS NOT NULL,
			COALESCE((SELECT jsonb_agg(jsonb_build_object('id', other.device_id::text, 'name', e.name) ORDER BY e.name)
				FROM device_identities other JOIN devices od ON od.entity_id = other.device_id AND od.merged_into IS NULL
				JOIN entities e ON e.id = other.device_id
				WHERE other.identity_type = i.identity_type AND lower(other.value) = lower(i.value) AND other.device_id <> i.device_id), '[]')
		FROM device_identities i WHERE i.device_id = $1::uuid
		ORDER BY CASE i.identity_type WHEN 'agent' THEN 0 WHEN 'machine' THEN 1 WHEN 'serial' THEN 2 WHEN 'mac' THEN 3
			WHEN 'hostname' THEN 5 WHEN 'ip' THEN 6 ELSE 4 END, i.confidence DESC, i.value`, deviceID)
	if err != nil {
		return IdentityReport{}, fmt.Errorf("query identity evidence: %w", err)
	}
	for rows.Next() {
		var item EvidenceItem
		if err := rows.Scan(&item.Type, &item.Value, &item.Source, &item.Confidence, &item.FirstSeenAt, &item.LastSeenAt,
			&item.ManuallyConfirmed, &item.SharedWith); err != nil {
			rows.Close()
			return IdentityReport{}, fmt.Errorf("scan identity evidence: %w", err)
		}
		if item.SharedWith == nil {
			item.SharedWith = []DeviceRef{}
		}
		classify(&item)
		report.Evidence = append(report.Evidence, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return IdentityReport{}, err
	}

	agentRows, err := db.Query(ctx, `SELECT id::text, display_name, status,
		COALESCE(status = 'active' AND last_seen_at > now() - interval '90 seconds'
			AND (last_shutdown_at IS NULL OR last_shutdown_at < last_seen_at), false), association_status, last_seen_at
		FROM agents WHERE device_id = $1::uuid ORDER BY display_name`, deviceID)
	if err != nil {
		return IdentityReport{}, fmt.Errorf("query device agents: %w", err)
	}
	for agentRows.Next() {
		var agent AgentRef
		if err := agentRows.Scan(&agent.ID, &agent.DisplayName, &agent.Status, &agent.Online, &agent.Association, &agent.LastSeenAt); err != nil {
			agentRows.Close()
			if err := agentRows.Err(); err != nil {
				return IdentityReport{}, fmt.Errorf("iterate device agents: %w", err)
			}
			return IdentityReport{}, err
		}
		report.Agents = append(report.Agents, agent)
	}
	agentRows.Close()

	matchRows, err := db.Query(ctx, `SELECT COALESCE(evidence->>'match', 'unrecorded'), count(*), max(observed_at)
		FROM observations WHERE entity_id = $1::uuid AND source = 'local-discovery' AND observed_at > now() - interval '30 days'
		GROUP BY 1 ORDER BY 2 DESC`, deviceID)
	if err != nil {
		return IdentityReport{}, fmt.Errorf("query discovery matches: %w", err)
	}
	for matchRows.Next() {
		var match MatchCount
		if err := matchRows.Scan(&match.Reason, &match.Count, &match.Last); err != nil {
			matchRows.Close()
			if err := matchRows.Err(); err != nil {
				return IdentityReport{}, fmt.Errorf("iterate discovery matches: %w", err)
			}
			return IdentityReport{}, err
		}
		report.Matches = append(report.Matches, match)
	}
	matchRows.Close()

	if report.Facts, err = ListAttributes(ctx, db, deviceID); err != nil {
		return IdentityReport{}, err
	}
	report.Strength, report.Summary = summarize(report.Evidence)
	return report, nil
}

func summarize(evidence []EvidenceItem) (string, string) {
	strength := StrengthNone
	rank := map[string]int{StrengthNone: 0, StrengthWeak: 1, StrengthModerate: 2, StrengthStrong: 3}
	reason := "No identity evidence has been recorded."
	for _, item := range evidence {
		if rank[item.Strength] > rank[strength] {
			strength = item.Strength
			switch item.Type {
			case "agent":
				reason = "Identified by an enrolled management agent."
			case "machine":
				reason = "Identified by an agent-reported machine ID."
			case "mac":
				reason = "Identified by MAC address " + item.Value + "."
			default:
				reason = "Identified by " + item.Type + " " + item.Value + "."
			}
		}
	}
	if strength == StrengthWeak {
		reason = "Only weak evidence (hostname or address); this record may describe more than one device over time."
	}
	return strength, reason
}
