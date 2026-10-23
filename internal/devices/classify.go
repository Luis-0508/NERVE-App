package devices

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SourceHeuristic marks classifications suggested by the rule table below.
const SourceHeuristic = "heuristic"

// ClassificationInputs is the observed evidence the rules look at: the
// effective manufacturer (an agent's claim or the OUI vendor), the effective
// operating system and the open TCP service ports.
type ClassificationInputs struct {
	Vendor          string
	OperatingSystem string
	OpenTCPPorts    []int
}

// ClassificationSuggestion is a rule's verdict with the reason shown to operators.
type ClassificationSuggestion struct {
	Classification string `json:"classification"`
	Source         string `json:"source"`
	Confidence     int    `json:"confidence"`
	Reason         string `json:"reason"`
}

type classificationRule struct {
	classification string
	confidence     int
	vendor         *regexp.Regexp // matched against the vendor
	os             *regexp.Regexp // matched against the operating system
	notOS          *regexp.Regexp // excludes an operating system the os pattern also matches
	ports          []int          // any one open TCP port matches
	reason         string
}

func vendorPattern(names ...string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(` + strings.Join(names, "|") + `)\b`)
}

// classificationRules suggest a type from evidence that is specific enough
// to be right most of the time. Vendors that make many kinds of devices (Cisco,
// HP, Apple, Microsoft, Raspberry Pi) have no rule on purpose. The most
// confident matching rule wins; on a tie, the earlier rule. Confidences stay
// below agent facts, and an operator's override always wins.
var classificationRules = []classificationRule{
	{classification: "server", confidence: 75, os: regexp.MustCompile(`(?i)\bwindows server\b`),
		reason: "The enrolled agent reports a Windows Server edition"},
	{classification: "virtual_machine", confidence: 70, vendor: vendorPattern("VMware", "Parallels"),
		reason: "The network adapter belongs to a virtualization vendor"},
	{classification: "hypervisor", confidence: 65, ports: []int{8006},
		reason: "TCP port 8006 (Proxmox VE web interface) is open"},
	{classification: "workstation", confidence: 60, os: regexp.MustCompile(`(?i)^windows\b`), notOS: regexp.MustCompile(`(?i)\bserver\b`),
		reason: "The enrolled agent reports a Windows client edition"},
	{classification: "camera", confidence: 55, vendor: vendorPattern("Hikvision", "Axis Communications", "Dahua Technology(CO)?"),
		reason: "The network adapter's vendor makes IP cameras"},
	{classification: "storage", confidence: 55, vendor: vendorPattern("Synology", "QNAP"),
		reason: "The network adapter's vendor makes network storage"},
	{classification: "printer", confidence: 50, vendor: vendorPattern("Brother Industries", "Seiko Epson", "Kyocera Document Solutions", "Lexmark", "Xerox"),
		reason: "The network adapter's vendor makes printers"},
	{classification: "media", confidence: 50, vendor: vendorPattern("Sonos", "Roku"),
		reason: "The network adapter's vendor makes media players"},
	{classification: "router", confidence: 45, vendor: vendorPattern("AVM GmbH", "AVM Audiovisuelles"),
		reason: "The network adapter's vendor makes routers (FRITZ!Box)"},
	{classification: "iot", confidence: 45, vendor: vendorPattern("Espressif", "Tuya", "Shelly"),
		reason: "The network adapter's vendor makes embedded IoT modules"},
	{classification: "printer", confidence: 45, ports: []int{9100, 631, 515},
		reason: "A printing port (9100, 631 or 515) is open"},
	{classification: "camera", confidence: 40, ports: []int{554},
		reason: "TCP port 554 (RTSP video streaming) is open"},
}

// Classify returns the most confident matching rule, or false when no rule
// matches and the device stays unclassified.
func Classify(inputs ClassificationInputs) (ClassificationSuggestion, bool) {
	var best *classificationRule
	for i := range classificationRules {
		rule := &classificationRules[i]
		if rule.matches(inputs) && (best == nil || rule.confidence > best.confidence) {
			best = rule
		}
	}
	if best == nil {
		return ClassificationSuggestion{}, false
	}
	return ClassificationSuggestion{Classification: best.classification, Source: SourceHeuristic, Confidence: best.confidence, Reason: best.reason}, true
}

func (rule classificationRule) matches(inputs ClassificationInputs) bool {
	switch {
	case rule.vendor != nil:
		return rule.vendor.MatchString(inputs.Vendor)
	case rule.os != nil:
		return rule.os.MatchString(inputs.OperatingSystem) && (rule.notOS == nil || !rule.notOS.MatchString(inputs.OperatingSystem))
	default:
		return slices.ContainsFunc(rule.ports, func(port int) bool { return slices.Contains(inputs.OpenTCPPorts, port) })
	}
}

// classificationInputAttributes are the attributes whose change re-runs the rules.
var classificationInputAttributes = []string{"manufacturer", "operating_system"}

// SuggestClassification evaluates the rules against the device's current evidence.
func SuggestClassification(ctx context.Context, db Querier, deviceID string) (ClassificationSuggestion, bool, error) {
	var inputs ClassificationInputs
	rows, err := db.Query(ctx, `SELECT COALESCE(d.manufacturer, ''), COALESCE(d.operating_system, ''),
		ARRAY(SELECT port FROM service_endpoints WHERE host_entity_id = d.entity_id AND protocol = 'tcp' AND state = 'open' ORDER BY port)
		FROM devices d WHERE d.entity_id = $1::uuid`, deviceID)
	if err != nil {
		return ClassificationSuggestion{}, false, fmt.Errorf("query classification evidence: %w", err)
	}
	found := false
	for rows.Next() {
		found = true
		if err := rows.Scan(&inputs.Vendor, &inputs.OperatingSystem, &inputs.OpenTCPPorts); err != nil {
			rows.Close()
			return ClassificationSuggestion{}, false, fmt.Errorf("scan classification evidence: %w", err)
		}
	}
	if err := rows.Err(); err != nil || !found {
		return ClassificationSuggestion{}, false, err
	}
	suggestion, ok := Classify(inputs)
	return suggestion, ok, nil
}

// RecordClassification stores the rules' current verdict as the device's
// `heuristic` classification claim, or removes that claim when no rule matches
// any more, so the discovered type falls back to other claims or `unknown`.
func RecordClassification(ctx context.Context, tx pgx.Tx, deviceID string) error {
	suggestion, ok, err := SuggestClassification(ctx, tx, deviceID)
	if err != nil {
		return err
	}
	if ok {
		if err := upsertAttribute(ctx, tx, deviceID, "classification", SourceHeuristic, suggestion.Classification, suggestion.Confidence); err != nil {
			return err
		}
		return RefreshDiscovered(ctx, tx, deviceID)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM device_attributes WHERE device_id = $1::uuid AND attribute = 'classification' AND source = $2`,
		deviceID, SourceHeuristic); err != nil {
		return fmt.Errorf("clear heuristic classification: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE devices d SET classification = COALESCE((SELECT value FROM device_attributes
		WHERE device_id = d.entity_id AND attribute = 'classification' ORDER BY confidence DESC, last_seen_at DESC LIMIT 1), 'unknown')
		WHERE d.entity_id = $1::uuid`, deviceID); err != nil {
		return fmt.Errorf("reset discovered classification: %w", err)
	}
	return nil
}

// BackfillClassifications re-runs the rules for every live device, so devices
// recorded before a rule existed or changed get its current verdict.
func BackfillClassifications(ctx context.Context, db *pgxpool.Pool) (int, error) {
	rows, err := db.Query(ctx, `SELECT entity_id::text FROM devices WHERE merged_into IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("query devices to classify: %w", err)
	}
	deviceIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("scan devices to classify: %w", err)
	}
	classified := 0
	var failures error
	for _, deviceID := range deviceIDs {
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return RecordClassification(ctx, tx, deviceID) })
		if err != nil {
			if ctx.Err() != nil {
				return classified, ctx.Err()
			}
			failures = errors.Join(failures, fmt.Errorf("device %s: %w", deviceID, err))
			continue
		}
		classified++
	}
	return classified, failures
}
