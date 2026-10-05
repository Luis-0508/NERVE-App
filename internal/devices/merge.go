package devices

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/audit"
)

var (
	ErrSameDevice    = errors.New("source and target must be different devices")
	ErrAgentConflict = errors.New("both devices have a live management agent; remove or reassign one first")
)

// MergeResult describes a completed merge.
type MergeResult struct {
	MergeID        string         `json:"mergeId"`
	SourceDeviceID string         `json:"sourceDeviceId"`
	TargetDeviceID string         `json:"targetDeviceId"`
	Summary        map[string]any `json:"summary"`
}

// mergedTrust is conservative: a blocked device stays blocked, otherwise the
// survivor gets the lower of the two trust states. Merging never raises trust.
func mergedTrust(source, target string) string {
	if source == TrustBlocked || target == TrustBlocked {
		return TrustBlocked
	}
	rank := map[string]int{TrustUnknown: 0, TrustReviewed: 1, TrustTrusted: 2}
	if rank[source] < rank[target] {
		return source
	}
	return target
}

// lockPair locks two devices in a fixed order so concurrent merges of the
// same pair cannot deadlock, and verifies that neither is retired.
func lockPair(ctx context.Context, tx pgx.Tx, a, b string) error {
	first, second := a, b
	if second < first {
		first, second = second, first
	}
	for _, id := range []string{first, second} {
		if err := lockDevice(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func reasonText(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 || strings.ContainsRune(reason, 0) {
		return "", fmt.Errorf("%w: reason must not exceed 500 characters", ErrInvalidInput)
	}
	return reason, nil
}

// Merge folds source into target in one transaction. Every child record moves
// to the target; the source row stays as a tombstone (merged_into), and a
// device_merges record keeps a snapshot of its human-owned metadata.
func (s *Service) Merge(ctx context.Context, sourceID, targetID, reason string, actor Actor) (MergeResult, error) {
	sourceID, targetID = strings.ToLower(strings.TrimSpace(sourceID)), strings.ToLower(strings.TrimSpace(targetID))
	if sourceID == targetID {
		return MergeResult{}, ErrSameDevice
	}
	reason, err := reasonText(reason)
	if err != nil {
		return MergeResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return MergeResult{}, fmt.Errorf("begin merge: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockPair(ctx, tx, sourceID, targetID); err != nil {
		return MergeResult{}, err
	}

	var sourceAgents, targetAgents int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE device_id = $1::uuid), count(*) FILTER (WHERE device_id = $2::uuid)
		FROM agents WHERE status <> 'removed' AND device_id IN ($1::uuid, $2::uuid)`, sourceID, targetID).Scan(&sourceAgents, &targetAgents); err != nil {
		return MergeResult{}, fmt.Errorf("check agents: %w", err)
	}
	if sourceAgents > 0 && targetAgents > 0 {
		return MergeResult{}, ErrAgentConflict
	}

	type owned struct{ name, displayName, notes, owner, location, classification, trust string }
	read := func(id string) (owned, error) {
		var value owned
		err := tx.QueryRow(ctx, `SELECT e.name, COALESCE(d.display_name, ''), COALESCE(d.notes, ''), COALESCE(d.owner, ''),
			COALESCE(d.location, ''), COALESCE(d.classification_override, ''), d.trust_state
			FROM devices d JOIN entities e ON e.id = d.entity_id WHERE d.entity_id = $1::uuid`, id).Scan(
			&value.name, &value.displayName, &value.notes, &value.owner, &value.location, &value.classification, &value.trust)
		return value, err
	}
	source, err := read(sourceID)
	if err != nil {
		return MergeResult{}, fmt.Errorf("read source device: %w", err)
	}
	target, err := read(targetID)
	if err != nil {
		return MergeResult{}, fmt.Errorf("read target device: %w", err)
	}

	moved := map[string]int64{}
	exec := func(key, sql string, args ...any) error {
		// Pass only the placeholders the statement uses; PostgreSQL rejects extras.
		if len(args) == 2 && !strings.Contains(sql, "$2") {
			args = args[:1]
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("merge %s: %w", key, err)
		}
		if key != "" {
			moved[key] += tag.RowsAffected()
		}
		return nil
	}
	steps := []struct {
		key, sql string
	}{
		// Interfaces keep their names unless the target already uses one.
		{"interfaces", `UPDATE network_interfaces ni SET device_id = $2::uuid,
			name = CASE WHEN EXISTS (SELECT 1 FROM network_interfaces t WHERE t.device_id = $2::uuid AND t.name = ni.name)
				THEN left(ni.name, 180) || '#' || left(ni.entity_id::text, 8) ELSE ni.name END
			WHERE ni.device_id = $1::uuid`},
		{"physicalPorts", `UPDATE physical_ports p SET device_id = $2::uuid,
			label = CASE WHEN EXISTS (SELECT 1 FROM physical_ports t WHERE t.device_id = $2::uuid AND t.label = p.label)
				THEN p.label || '#' || left(p.entity_id::text, 8) ELSE p.label END
			WHERE p.device_id = $1::uuid`},
		{"hypervisors", `UPDATE hypervisors SET device_id = $2::uuid WHERE device_id = $1::uuid`},
		{"containerHosts", `UPDATE container_hosts SET device_id = $2::uuid WHERE device_id = $1::uuid`},
		{"discoveryBindings", `UPDATE discovery_bindings SET device_id = $2::uuid WHERE device_id = $1::uuid`},
		// Identity evidence: combine first/last seen and keep the stronger claim.
		{"identities", `INSERT INTO device_identities (device_id, identity_type, value, source, confidence, first_seen_at, last_seen_at, manually_confirmed_at)
			SELECT $2::uuid, identity_type, value, source, confidence, first_seen_at, last_seen_at, manually_confirmed_at
			FROM device_identities WHERE device_id = $1::uuid
			ON CONFLICT (identity_type, value, device_id) DO UPDATE SET
				confidence = GREATEST(device_identities.confidence, EXCLUDED.confidence),
				first_seen_at = LEAST(device_identities.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(device_identities.last_seen_at, EXCLUDED.last_seen_at),
				manually_confirmed_at = COALESCE(device_identities.manually_confirmed_at, EXCLUDED.manually_confirmed_at)`},
		{"", `DELETE FROM device_identities WHERE device_id = $1::uuid`},
		{"facts", `INSERT INTO device_attributes (device_id, attribute, source, value, confidence, first_seen_at, last_seen_at)
			SELECT $2::uuid, attribute, source, value, confidence, first_seen_at, last_seen_at FROM device_attributes WHERE device_id = $1::uuid
			ON CONFLICT (device_id, attribute, source) DO UPDATE SET
				value = CASE WHEN EXCLUDED.last_seen_at > device_attributes.last_seen_at THEN EXCLUDED.value ELSE device_attributes.value END,
				confidence = CASE WHEN EXCLUDED.last_seen_at > device_attributes.last_seen_at THEN EXCLUDED.confidence ELSE device_attributes.confidence END,
				first_seen_at = LEAST(device_attributes.first_seen_at, EXCLUDED.first_seen_at),
				last_seen_at = GREATEST(device_attributes.last_seen_at, EXCLUDED.last_seen_at)`},
		{"", `DELETE FROM device_attributes WHERE device_id = $1::uuid`},
		{"tags", `INSERT INTO device_tags (device_id, tag, created_at, created_by)
			SELECT $2::uuid, tag, created_at, created_by FROM device_tags WHERE device_id = $1::uuid ON CONFLICT DO NOTHING`},
		{"", `DELETE FROM device_tags WHERE device_id = $1::uuid`},
		// Service endpoints on the same protocol/port collapse into the target's,
		// which inherits the earliest first-seen and latest last-seen.
		{"", `UPDATE service_endpoints t SET first_seen_at = LEAST(t.first_seen_at, s.first_seen_at), last_seen_at = GREATEST(t.last_seen_at, s.last_seen_at)
			FROM service_endpoints s WHERE s.host_entity_id = $1::uuid AND t.host_entity_id = $2::uuid AND t.protocol = s.protocol AND t.port = s.port`},
		{"servicesCombined", `DELETE FROM entities WHERE id IN (SELECT s.entity_id FROM service_endpoints s JOIN service_endpoints t
			ON t.host_entity_id = $2::uuid AND t.protocol = s.protocol AND t.port = s.port WHERE s.host_entity_id = $1::uuid)`},
		{"services", `UPDATE service_endpoints SET host_entity_id = $2::uuid WHERE host_entity_id = $1::uuid`},
		// Relationships between the two would become self-references; duplicates collapse.
		{"relationshipsDropped", `DELETE FROM relationships WHERE (source_entity_id = $1::uuid AND target_entity_id = $2::uuid)
			OR (source_entity_id = $2::uuid AND target_entity_id = $1::uuid)`},
		{"", `DELETE FROM relationships r WHERE r.source_entity_id = $1::uuid AND EXISTS (SELECT 1 FROM relationships t
			WHERE t.source_entity_id = $2::uuid AND t.target_entity_id = r.target_entity_id AND t.relationship_type = r.relationship_type AND t.source = r.source)`},
		{"", `DELETE FROM relationships r WHERE r.target_entity_id = $1::uuid AND EXISTS (SELECT 1 FROM relationships t
			WHERE t.target_entity_id = $2::uuid AND t.source_entity_id = r.source_entity_id AND t.relationship_type = r.relationship_type AND t.source = r.source)`},
		{"relationships", `UPDATE relationships SET source_entity_id = $2::uuid WHERE source_entity_id = $1::uuid`},
		{"relationships", `UPDATE relationships SET target_entity_id = $2::uuid WHERE target_entity_id = $1::uuid`},
		// History moves with the device.
		{"observations", `UPDATE observations SET entity_id = $2::uuid WHERE entity_id = $1::uuid`},
		{"events", `UPDATE events SET entity_id = $2::uuid WHERE entity_id = $1::uuid`},
		{"", `DELETE FROM metrics m WHERE m.entity_id = $1::uuid AND EXISTS (SELECT 1 FROM metrics t
			WHERE t.entity_id = $2::uuid AND t.metric = m.metric AND t.observed_at = m.observed_at AND t.source = m.source)`},
		{"metrics", `UPDATE metrics SET entity_id = $2::uuid WHERE entity_id = $1::uuid`},
		// The management agent keeps its association status and evidence.
		{"agents", `UPDATE agents SET device_id = $2::uuid, updated_at = now() WHERE device_id = $1::uuid`},
		{"enrollments", `UPDATE agent_enrollments SET device_id = $2::uuid WHERE device_id = $1::uuid`},
		// Earlier merges into the source now point at the survivor.
		{"previousMerges", `UPDATE devices SET merged_into = $2::uuid WHERE merged_into = $1::uuid`},
	}
	for _, step := range steps {
		if err := exec(step.key, step.sql, sourceID, targetID); err != nil {
			return MergeResult{}, err
		}
	}

	// Human-owned metadata: the target's values win; blanks are filled from
	// the source. The source snapshot is kept in the merge record.
	fill := func(targetValue, sourceValue string) string {
		if targetValue != "" {
			return targetValue
		}
		return sourceValue
	}
	trust := mergedTrust(source.trust, target.trust)
	if err := exec("", `UPDATE devices SET display_name = NULLIF($3, ''), notes = NULLIF($4, ''), owner = NULLIF($5, ''),
		location = NULLIF($6, ''), classification_override = NULLIF($7, ''),
		metadata_updated_at = clock_timestamp(), metadata_updated_by = $8::uuid,
		trust_state = $9, trust_changed_at = CASE WHEN trust_state = $9 THEN trust_changed_at ELSE clock_timestamp() END,
		trust_changed_by = CASE WHEN trust_state = $9 THEN trust_changed_by ELSE $8::uuid END
		WHERE entity_id = $2::uuid AND $1::uuid IS NOT NULL`, sourceID, targetID,
		fill(target.displayName, source.displayName), fill(target.notes, source.notes), fill(target.owner, source.owner),
		fill(target.location, source.location), fill(target.classification, source.classification), actor.UserID, trust); err != nil {
		return MergeResult{}, err
	}
	if err := RefreshDiscovered(ctx, tx, targetID); err != nil {
		return MergeResult{}, err
	}
	if err := exec("", `UPDATE devices SET merged_into = $2::uuid, merged_at = now() WHERE entity_id = $1::uuid`, sourceID, targetID); err != nil {
		return MergeResult{}, err
	}
	if err := exec("", `UPDATE entities SET status = 'offline', updated_at = now() WHERE id = $1::uuid`, sourceID, targetID); err != nil {
		return MergeResult{}, err
	}
	if err := exec("", `DELETE FROM device_duplicate_dismissals WHERE device_a IN ($1::uuid, $2::uuid) OR device_b IN ($1::uuid, $2::uuid)`, sourceID, targetID); err != nil {
		return MergeResult{}, err
	}

	summary := map[string]any{"moved": moved, "trust": map[string]string{"source": source.trust, "target": target.trust, "result": trust},
		"source": map[string]string{"name": source.name, "displayName": source.displayName, "owner": source.owner, "location": source.location,
			"classification": source.classification, "notesLength": fmt.Sprint(utf8.RuneCountInString(source.notes))}}
	var mergeID string
	if err := tx.QueryRow(ctx, `INSERT INTO device_merges (source_device_id, target_device_id, merged_by, reason, summary)
		VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, ''), $5) RETURNING id::text`,
		sourceID, targetID, actor.UserID, reason, summary).Scan(&mergeID); err != nil {
		return MergeResult{}, fmt.Errorf("record merge: %w", err)
	}
	params := map[string]any{"mergeId": mergeID, "sourceDeviceId": sourceID, "targetDeviceId": targetID, "moved": moved,
		"trust": map[string]string{"source": source.trust, "target": target.trust, "result": trust}}
	if reason != "" {
		params["reason"] = reason
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "device.merged", TargetType: "device", TargetID: targetID,
		SourceAddress: actor.SourceAddress, Parameters: params}); err != nil {
		return MergeResult{}, err
	}
	if err := exec("", `INSERT INTO events (entity_id, type, severity, category, message, details)
		VALUES ($2::uuid, 'device.merged', 'info', 'identity', 'Another device record was merged into this device.',
		jsonb_build_object('sourceDeviceId', $1::text))`, sourceID, targetID); err != nil {
		return MergeResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MergeResult{}, fmt.Errorf("commit merge: %w", err)
	}
	return MergeResult{MergeID: mergeID, SourceDeviceID: sourceID, TargetDeviceID: targetID, Summary: summary}, nil
}
