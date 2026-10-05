package devices

import (
	"context"
	"fmt"
	"sort"

	"github.com/nerve-labs/nerve/internal/audit"
)

// Duplicate candidate confidence. Candidates are suggestions only: nothing is
// merged until an operator chooses source and target.
const (
	DuplicateHigh   = "high"
	DuplicateMedium = "medium"
)

type DuplicateSignal struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	FromAgent bool   `json:"fromAgent"`
}

type DuplicateCandidate struct {
	DeviceA    DeviceRef         `json:"deviceA"`
	DeviceB    DeviceRef         `json:"deviceB"`
	Confidence string            `json:"confidence"`
	Reasons    []string          `json:"reasons"`
	Signals    []DuplicateSignal `json:"signals"`
}

// classifyDuplicate turns shared evidence into a confidence and readable
// reasons. Weak-only overlaps (one hostname or one address) are not reported.
func classifyDuplicate(signals []DuplicateSignal) (string, []string) {
	var reasons []string
	high, medium := false, false
	hostname, ip := false, false
	for _, signal := range signals {
		switch signal.Type {
		case "machine":
			high = true
			reasons = append(reasons, "Same agent-reported machine ID")
		case "serial":
			high = true
			reasons = append(reasons, "Same serial number "+signal.Value)
		case "mac":
			if locallyAdministered(signal.Value) {
				medium = true
				reasons = append(reasons, "Same locally administered MAC "+signal.Value)
			} else {
				high = true
				reasons = append(reasons, "Same MAC address "+signal.Value)
			}
		case "hostname":
			hostname = true
		case "ip":
			ip = true
			if signal.FromAgent {
				medium = true
				reasons = append(reasons, "An enrolled agent reports address "+signal.Value+", which the other record holds or held recently")
			}
		}
	}
	if hostname && ip {
		medium = true
		reasons = append(reasons, "Same hostname and a shared recent address")
	}
	switch {
	case high:
		return DuplicateHigh, reasons
	case medium:
		return DuplicateMedium, reasons
	default:
		return "", nil
	}
}

// Duplicates lists likely duplicate pairs among live devices, optionally only
// those involving deviceID. Dismissed pairs are excluded.
func (s *Service) Duplicates(ctx context.Context, deviceID string, limit int) ([]DuplicateCandidate, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `WITH live AS (SELECT entity_id FROM devices WHERE merged_into IS NULL),
		signals AS (
			SELECT i.device_id, i.identity_type AS type, lower(i.value) AS value, i.source = 'nerve-agent' AS from_agent
			FROM device_identities i JOIN live ON live.entity_id = i.device_id
			WHERE i.identity_type IN ('mac', 'machine', 'serial', 'hostname', 'ip')
			UNION
			SELECT ni.device_id, 'ip', host(ip.address), false
			FROM ip_addresses ip JOIN interface_addresses ia ON ia.ip_address_id = ip.entity_id
			JOIN network_interfaces ni ON ni.entity_id = ia.interface_id JOIN live ON live.entity_id = ni.device_id
			-- Recent address history counts too: a record that held an address an
			-- enrolled agent now reports is a likely duplicate (DHCP churn).
			WHERE ip.valid_until IS NULL OR ip.valid_until > now() - interval '30 days'
		),
		pairs AS (
			SELECT a.device_id AS device_a, b.device_id AS device_b,
				jsonb_agg(DISTINCT jsonb_build_object('type', a.type, 'value', a.value, 'fromAgent', a.from_agent OR b.from_agent)) AS signals
			FROM signals a JOIN signals b ON a.type = b.type AND a.value = b.value AND a.device_id < b.device_id
			WHERE (NULLIF($1, '') IS NULL OR a.device_id = NULLIF($1, '')::uuid OR b.device_id = NULLIF($1, '')::uuid)
			AND NOT EXISTS (SELECT 1 FROM device_duplicate_dismissals x WHERE x.device_a = a.device_id AND x.device_b = b.device_id)
			GROUP BY 1, 2
		)
		SELECT p.device_a::text, ea.name, p.device_b::text, eb.name, p.signals
		FROM pairs p JOIN entities ea ON ea.id = p.device_a JOIN entities eb ON eb.id = p.device_b
		ORDER BY ea.name, eb.name LIMIT 1000`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query duplicate candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]DuplicateCandidate, 0)
	for rows.Next() {
		var candidate DuplicateCandidate
		if err := rows.Scan(&candidate.DeviceA.ID, &candidate.DeviceA.Name, &candidate.DeviceB.ID, &candidate.DeviceB.Name, &candidate.Signals); err != nil {
			return nil, fmt.Errorf("scan duplicate candidate: %w", err)
		}
		candidate.Confidence, candidate.Reasons = classifyDuplicate(candidate.Signals)
		if candidate.Confidence != "" {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Confidence == DuplicateHigh && candidates[j].Confidence != DuplicateHigh
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

// DismissDuplicate records that two devices are distinct.
func (s *Service) DismissDuplicate(ctx context.Context, a, b, reason string, actor Actor) error {
	if a == b {
		return ErrSameDevice
	}
	if b < a {
		a, b = b, a
	}
	reason, err := reasonText(reason)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin dismissal: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockPair(ctx, tx, a, b); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO device_duplicate_dismissals (device_a, device_b, dismissed_by, reason)
		VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, '')) ON CONFLICT DO NOTHING`, a, b, actor.UserID, reason); err != nil {
		return fmt.Errorf("record dismissal: %w", err)
	}
	params := map[string]any{"deviceA": a, "deviceB": b}
	if reason != "" {
		params["reason"] = reason
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "device.duplicate.dismissed", TargetType: "device", TargetID: a,
		SourceAddress: actor.SourceAddress, Parameters: params}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
