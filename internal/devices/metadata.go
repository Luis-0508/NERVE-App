// Package devices owns device identity: human-owned metadata, trust state,
// provenance of discovered facts, identity evidence and merge/split.
//
// Discovered facts (written by discovery and agents) and human-owned metadata
// (written only through this package by an identified operator) are kept
// apart. PostgreSQL triggers reject owned-field changes that lack an operator
// stamp, so discovery cannot overwrite operator decisions even by mistake.
package devices

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/audit"
)

var (
	ErrNotFound     = errors.New("device not found")
	ErrInvalidInput = errors.New("invalid device update")
	ErrMerged       = errors.New("device was merged into another device")
)

// Trust states are operator-owned. Discovery never changes them, and nothing
// promotes a device automatically.
const (
	TrustUnknown  = "unknown"
	TrustReviewed = "reviewed"
	TrustTrusted  = "trusted"
	TrustBlocked  = "blocked"
)

var TrustStates = []string{TrustUnknown, TrustReviewed, TrustTrusted, TrustBlocked}

// Classifications is the vocabulary for operator classification overrides.
var Classifications = []string{
	"workstation", "laptop", "server", "virtual_machine", "router", "switch", "access_point",
	"firewall", "network", "printer", "phone", "tablet", "iot", "camera", "storage", "media",
	"hypervisor", "container_host", "other",
}

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,47}$`)

const maxTags = 32

// Actor identifies the operator making a change.
type Actor struct {
	UserID        string
	SourceAddress string
}

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// MetadataUpdate changes human-owned fields. A nil field is left unchanged; a
// pointer to an empty string clears the value (and the manual override).
type MetadataUpdate struct {
	DisplayName    *string
	Notes          *string
	Owner          *string
	Location       *string
	Classification *string
	Tags           *[]string
}

func (u MetadataUpdate) empty() bool {
	return u.DisplayName == nil && u.Notes == nil && u.Owner == nil && u.Location == nil && u.Classification == nil && u.Tags == nil
}

func cleanText(value *string, limit int, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	cleaned := strings.TrimSpace(*value)
	if !utf8.ValidString(cleaned) || strings.ContainsRune(cleaned, 0) {
		return nil, fmt.Errorf("%w: %s must be valid text", ErrInvalidInput, field)
	}
	if utf8.RuneCountInString(cleaned) > limit {
		return nil, fmt.Errorf("%w: %s must not exceed %d characters", ErrInvalidInput, field, limit)
	}
	return &cleaned, nil
}

// NormalizeTags lower-cases, deduplicates and validates tags.
func NormalizeTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || seen[tag] {
			continue
		}
		if !tagPattern.MatchString(tag) {
			return nil, fmt.Errorf("%w: tag %q must be 1-48 characters of a-z, 0-9, '.', '_', ':' or '-'", ErrInvalidInput, raw)
		}
		seen[tag] = true
		result = append(result, tag)
	}
	if len(result) > maxTags {
		return nil, fmt.Errorf("%w: at most %d tags", ErrInvalidInput, maxTags)
	}
	sort.Strings(result)
	return result, nil
}

func validClassification(value string) bool {
	for _, item := range Classifications {
		if item == value {
			return true
		}
	}
	return false
}

func lockDevice(ctx context.Context, tx pgx.Tx, deviceID string) error {
	var merged *string
	err := tx.QueryRow(ctx, `SELECT merged_into::text FROM devices WHERE entity_id = $1::uuid FOR UPDATE`, deviceID).Scan(&merged)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock device: %w", err)
	}
	if merged != nil {
		return ErrMerged
	}
	return nil
}

// UpdateMetadata applies operator edits and audits which fields changed.
// Free-text notes are audited as "changed" with their length only.
func (s *Service) UpdateMetadata(ctx context.Context, deviceID string, update MetadataUpdate, actor Actor) error {
	if update.empty() {
		return fmt.Errorf("%w: nothing to change", ErrInvalidInput)
	}
	var err error
	if update.DisplayName, err = cleanText(update.DisplayName, 200, "display name"); err != nil {
		return err
	}
	if update.Notes, err = cleanText(update.Notes, 10000, "notes"); err != nil {
		return err
	}
	if update.Owner, err = cleanText(update.Owner, 200, "owner"); err != nil {
		return err
	}
	if update.Location, err = cleanText(update.Location, 200, "location"); err != nil {
		return err
	}
	if update.Classification != nil {
		value := strings.TrimSpace(*update.Classification)
		if value != "" && !validClassification(value) {
			return fmt.Errorf("%w: unknown classification %q", ErrInvalidInput, value)
		}
		update.Classification = &value
	}
	var tags []string
	if update.Tags != nil {
		if tags, err = NormalizeTags(*update.Tags); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin device update: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockDevice(ctx, tx, deviceID); err != nil {
		return err
	}
	var current struct{ displayName, notes, owner, location, classification string }
	if err := tx.QueryRow(ctx, `SELECT COALESCE(display_name, ''), COALESCE(notes, ''), COALESCE(owner, ''), COALESCE(location, ''),
		COALESCE(classification_override, '') FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(
		&current.displayName, &current.notes, &current.owner, &current.location, &current.classification); err != nil {
		return fmt.Errorf("read device metadata: %w", err)
	}
	changes := map[string]any{}
	pick := func(name string, value *string, old string) string {
		if value == nil || *value == old {
			return old
		}
		if name == "notes" {
			changes[name] = map[string]any{"changed": true, "length": utf8.RuneCountInString(*value)}
		} else {
			changes[name] = map[string]any{"from": old, "to": *value}
		}
		return *value
	}
	displayName := pick("displayName", update.DisplayName, current.displayName)
	notes := pick("notes", update.Notes, current.notes)
	owner := pick("owner", update.Owner, current.owner)
	location := pick("location", update.Location, current.location)
	classification := pick("classification", update.Classification, current.classification)

	if len(changes) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE devices SET display_name = NULLIF($2, ''), notes = NULLIF($3, ''), owner = NULLIF($4, ''),
			location = NULLIF($5, ''), classification_override = NULLIF($6, ''), metadata_updated_at = clock_timestamp(),
			metadata_updated_by = $7::uuid WHERE entity_id = $1::uuid`,
			deviceID, displayName, notes, owner, location, classification, actor.UserID); err != nil {
			return fmt.Errorf("update device metadata: %w", err)
		}
	}
	if update.Tags != nil {
		var previous []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(tag ORDER BY tag), '{}') FROM device_tags WHERE device_id = $1::uuid`, deviceID).Scan(&previous); err != nil {
			return fmt.Errorf("read device tags: %w", err)
		}
		if strings.Join(previous, ",") != strings.Join(tags, ",") {
			if _, err := tx.Exec(ctx, `DELETE FROM device_tags WHERE device_id = $1::uuid AND NOT (tag = ANY($2))`, deviceID, tags); err != nil {
				return fmt.Errorf("remove device tags: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO device_tags (device_id, tag, created_by) SELECT $1::uuid, tag, $3::uuid FROM unnest($2::text[]) tag
				ON CONFLICT DO NOTHING`, deviceID, tags, actor.UserID); err != nil {
				return fmt.Errorf("add device tags: %w", err)
			}
			changes["tags"] = map[string]any{"from": previous, "to": tags}
		}
	}
	if len(changes) == 0 {
		return tx.Commit(ctx)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "device.metadata.changed", TargetType: "device",
		TargetID: deviceID, SourceAddress: actor.SourceAddress, Parameters: changes}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit device update: %w", err)
	}
	return nil
}

// SetTrust changes the operator-owned trust state.
func (s *Service) SetTrust(ctx context.Context, deviceID, state, reason string, actor Actor) error {
	valid := false
	for _, item := range TrustStates {
		valid = valid || item == state
	}
	if !valid {
		return fmt.Errorf("%w: trust state must be one of %s", ErrInvalidInput, strings.Join(TrustStates, ", "))
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 || strings.ContainsRune(reason, 0) {
		return fmt.Errorf("%w: reason must not exceed 500 characters", ErrInvalidInput)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin trust change: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := lockDevice(ctx, tx, deviceID); err != nil {
		return err
	}
	var previous string
	if err := tx.QueryRow(ctx, `SELECT trust_state FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(&previous); err != nil {
		return fmt.Errorf("read trust state: %w", err)
	}
	if previous == state {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE devices SET trust_state = $2, trust_changed_at = clock_timestamp(), trust_changed_by = $3::uuid
		WHERE entity_id = $1::uuid`, deviceID, state, actor.UserID); err != nil {
		return fmt.Errorf("update trust state: %w", err)
	}
	params := map[string]any{"from": previous, "to": state}
	if reason != "" {
		params["reason"] = reason
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "device.trust.changed", TargetType: "device",
		TargetID: deviceID, SourceAddress: actor.SourceAddress, Parameters: params}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details)
		VALUES ($1::uuid, 'device.trust.changed', CASE WHEN $3 = 'blocked' THEN 'warning' ELSE 'info' END, 'identity',
		'Device trust state changed from ' || $2 || ' to ' || $3 || '.', jsonb_build_object('from', $2::text, 'to', $3::text))`,
		deviceID, previous, state); err != nil {
		return fmt.Errorf("record trust event: %w", err)
	}
	return tx.Commit(ctx)
}

// Attribute is one discovered fact with its source and confidence.
type Attribute struct {
	Attribute   string    `json:"attribute"`
	Source      string    `json:"source"`
	Value       string    `json:"value"`
	Confidence  int       `json:"confidence"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	Effective   bool      `json:"effective"`
}

// Sources and default confidence for discovered attributes.
const (
	SourceReverseDNS = "reverse-dns"
	SourceAgent      = "nerve-agent"
	SourceOUI        = "oui"
)
