//go:build integration

package devices_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// scan runs one real discovery scan with the given observations.
func scan(t *testing.T, pool *pgxpool.Pool, userID string, observations ...discovery.Observation) {
	t.Helper()
	ctx := context.Background()
	repo := discovery.NewRepository(pool)
	target, err := repo.SaveTarget(ctx, "Test network", "192.0.2.0/24", userID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Queue(ctx, target.ID, userID, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.Heartbeat(ctx, discovery.AgentHeartbeat{ID: "test-agent", Hostname: "test", Platform: "test", Version: "test", Capabilities: []string{"icmp"}}); err != nil {
		t.Fatal(err)
	}
	job, err := repo.Claim(ctx, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Report(ctx, job.ID, "test-agent", discovery.Report{Observations: observations}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, job.ID, "test-agent", discovery.Completion{AddressesTotal: 254, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
}

func deviceByMAC(t *testing.T, pool *pgxpool.Pool, mac string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT device_id::text FROM device_identities WHERE identity_type = 'mac' AND value = $1`, mac).Scan(&id); err != nil {
		t.Fatalf("device for %s: %v", mac, err)
	}
	return id
}

func strPtr(value string) *string { return &value }

func auditActions(t *testing.T, pool *pgxpool.Pool, deviceID string) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT action FROM audit_log WHERE target_type = 'device' AND target_id = $1`, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var action string
		_ = rows.Scan(&action)
		counts[action]++
	}
	return counts
}

func TestDiscoveryNeverOverwritesHumanOwnedMetadata(t *testing.T) {
	pool := testsupport.MigratedPool(t, "devices_owned")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	service := devices.NewService(pool)
	actor := devices.Actor{UserID: adminID}
	inv := inventory.NewRepositories(pool).Devices

	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.10", Reachable: true, Hostname: "desktop-abc123", MAC: "02:00:00:00:00:10", Method: "icmp"})
	deviceID := deviceByMAC(t, pool, "02:00:00:00:00:10")

	err := service.UpdateMetadata(ctx, deviceID, devices.MetadataUpdate{DisplayName: strPtr("Luis-PC"), Notes: strPtr("Desk under the window"),
		Classification: strPtr("workstation"), Tags: &[]string{"Office", "office", "critical"}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetTrust(ctx, deviceID, devices.TrustTrusted, "known workstation", actor); err != nil {
		t.Fatal(err)
	}

	// A later scan reports a new reverse-DNS name.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.10", Reachable: true, Hostname: "desktop-renamed", MAC: "02:00:00:00:00:10", Method: "icmp"})
	device, err := inv.Get(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if device.Name != "Luis-PC" || device.DisplayName != "Luis-PC" || device.DiscoveredName != "desktop-renamed" || device.Hostname != "desktop-renamed" {
		t.Fatalf("names after rescan: effective=%q manual=%q discovered=%q hostname=%q", device.Name, device.DisplayName, device.DiscoveredName, device.Hostname)
	}
	if device.Classification != "workstation" || device.DiscoveredClassification != "unknown" || !device.ClassificationOverridden {
		t.Fatalf("classification: %+v", device.DeviceSummary)
	}
	if device.TrustState != "trusted" || device.Notes != "Desk under the window" || strings.Join(device.Tags, ",") != "critical,office" {
		t.Fatalf("owned fields changed by discovery: %+v", device)
	}

	// PostgreSQL itself refuses owned-field writes without an operator stamp,
	// which is how discovery or agent code paths would write.
	for name, statement := range map[string]string{
		"display name":   `UPDATE devices SET display_name = 'hijacked' WHERE entity_id = $1::uuid`,
		"trust":          `UPDATE devices SET trust_state = 'blocked' WHERE entity_id = $1::uuid`,
		"classification": `UPDATE devices SET classification_override = 'printer' WHERE entity_id = $1::uuid`,
		"notes":          `UPDATE devices SET notes = 'x' WHERE entity_id = $1::uuid`,
	} {
		if _, err := pool.Exec(ctx, statement, deviceID); err == nil {
			t.Fatalf("unstamped %s change was accepted", name)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE entities SET name = 'renamed-by-discovery' WHERE id = $1::uuid`, deviceID); err != nil {
		t.Fatal(err)
	}
	if device, _ := inv.Get(ctx, deviceID); device.Name != "Luis-PC" {
		t.Fatalf("entity name write overrode the manual name: %q", device.Name)
	}

	// Clearing the manual name falls back to the discovered name.
	if err := service.UpdateMetadata(ctx, deviceID, devices.MetadataUpdate{DisplayName: strPtr(""), Classification: strPtr("")}, actor); err != nil {
		t.Fatal(err)
	}
	if device, _ := inv.Get(ctx, deviceID); device.Name != "desktop-renamed" || device.DisplayName != "" || device.ClassificationOverridden {
		t.Fatalf("after clearing overrides: %+v", device.DeviceSummary)
	}

	counts := auditActions(t, pool, deviceID)
	if counts["device.metadata.changed"] != 2 || counts["device.trust.changed"] != 1 {
		t.Fatalf("audit records: %v", counts)
	}
	var notesParams string
	_ = pool.QueryRow(ctx, `SELECT safe_parameters::text FROM audit_log WHERE action = 'device.metadata.changed' AND target_id = $1 ORDER BY id LIMIT 1`, deviceID).Scan(&notesParams)
	if strings.Contains(notesParams, "window") || !strings.Contains(notesParams, `"length"`) {
		t.Fatalf("notes content leaked into the audit log: %s", notesParams)
	}
}

func TestMetadataValidationAndTrust(t *testing.T) {
	pool := testsupport.MigratedPool(t, "devices_validate")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	service := devices.NewService(pool)
	actor := devices.Actor{UserID: adminID}
	deviceID := testsupport.CreateDevice(t, pool, "nas", "02:00:00:00:00:20", "192.0.2.20")

	for name, update := range map[string]devices.MetadataUpdate{
		"empty":        {},
		"bad tag":      {Tags: &[]string{"has space"}},
		"bad class":    {Classification: strPtr("spaceship")},
		"long name":    {DisplayName: strPtr(strings.Repeat("n", 201))},
		"nul in notes": {Notes: strPtr("a\x00b")},
		"too many tags": {Tags: func() *[]string {
			tags := make([]string, 33)
			for i := range tags {
				tags[i] = "t" + strings.Repeat("x", i)
			}
			return &tags
		}()},
	} {
		if err := service.UpdateMetadata(ctx, deviceID, update, actor); !errors.Is(err, devices.ErrInvalidInput) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	if err := service.SetTrust(ctx, deviceID, "promoted", "", actor); !errors.Is(err, devices.ErrInvalidInput) {
		t.Fatalf("invalid trust state: %v", err)
	}
	if err := service.SetTrust(ctx, "00000000-0000-4000-8000-000000000000", devices.TrustTrusted, "", actor); !errors.Is(err, devices.ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}
	// Setting the same state is a no-op and is not audited twice.
	_ = service.SetTrust(ctx, deviceID, devices.TrustBlocked, "lost", actor)
	_ = service.SetTrust(ctx, deviceID, devices.TrustBlocked, "lost", actor)
	if counts := auditActions(t, pool, deviceID); counts["device.trust.changed"] != 1 {
		t.Fatalf("trust audit: %v", counts)
	}
	var stampedBy string
	_ = pool.QueryRow(ctx, `SELECT trust_changed_by::text FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(&stampedBy)
	if stampedBy != adminID {
		t.Fatal("trust change is not attributed to the operator")
	}
}

func TestAttributeProvenancePrefersConfidentSources(t *testing.T) {
	pool := testsupport.MigratedPool(t, "devices_provenance")
	ctx := context.Background()
	deviceID := testsupport.CreateDevice(t, pool, "printer", "02:00:00:00:00:30", "192.0.2.30")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.RecordAttribute(ctx, tx, deviceID, "hostname", devices.SourceReverseDNS, "printer.lan", devices.ConfidenceReverseDNS); err != nil {
		t.Fatal(err)
	}
	if err := devices.RecordAttribute(ctx, tx, deviceID, "hostname", devices.SourceAgent, "PRN-01", devices.ConfidenceAgent); err != nil {
		t.Fatal(err)
	}
	if err := devices.RecordAttribute(ctx, tx, deviceID, "operating_system", devices.SourceAgent, "Linux", devices.ConfidenceAgent); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	device, _ := inventory.NewRepositories(pool).Devices.Get(ctx, deviceID)
	if device.Hostname != "PRN-01" || device.OperatingSystem != "Linux" || device.Name != "PRN-01" {
		t.Fatalf("effective discovered values: hostname=%q os=%q name=%q", device.Hostname, device.OperatingSystem, device.Name)
	}
	facts, err := devices.ListAttributes(ctx, pool, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	effective := map[string]string{}
	for _, fact := range facts {
		if fact.Effective {
			effective[fact.Attribute] = fact.Source
		}
	}
	if len(facts) != 3 || effective["hostname"] != devices.SourceAgent {
		t.Fatalf("facts: %+v", facts)
	}
}

func TestInventorySearchUsesManualMetadataAndAllAddresses(t *testing.T) {
	pool := testsupport.MigratedPool(t, "devices_search")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	service := devices.NewService(pool)
	actor := devices.Actor{UserID: adminID}

	scan(t, pool, adminID,
		discovery.Observation{Address: "192.0.2.40", Reachable: true, Hostname: "host-a", MAC: "02:00:00:00:00:40", Method: "icmp"},
		discovery.Observation{Address: "192.0.2.41", Reachable: true, Hostname: "host-b", MAC: "02:00:00:00:00:41", Method: "icmp"})
	a := deviceByMAC(t, pool, "02:00:00:00:00:40")
	if err := service.UpdateMetadata(ctx, a, devices.MetadataUpdate{DisplayName: strPtr("Accounting NAS"), Tags: &[]string{"finance"}}, actor); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTrust(ctx, a, devices.TrustReviewed, "", actor); err != nil {
		t.Fatal(err)
	}
	// The same device moves to a new address; the old one stays in history.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.140", Reachable: true, MAC: "02:00:00:00:00:40", Method: "icmp"})

	inv := inventory.NewRepositories(pool).Devices
	for search, want := range map[string]int{"accounting": 1, "finance": 1, "00:00:00:40": 1, "192.0.2.140": 1, "192.0.2.40": 1, "host-": 2, "00_00": 0, "%": 0} {
		params, _ := inventory.NormalizeListParams(inventory.DeviceListParams{Search: search})
		list, err := inv.List(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		if list.Total != want || (want == 1 && list.Items[0].ID != a) {
			t.Fatalf("search %q: got %d results, want %d", search, list.Total, want)
		}
	}
	params, _ := inventory.NormalizeListParams(inventory.DeviceListParams{Trust: "reviewed", Tag: "Finance"})
	if list, _ := inv.List(ctx, params); list.Total != 1 || list.Items[0].Name != "Accounting NAS" || list.Items[0].DiscoveredName != "host-a" {
		t.Fatalf("trust/tag filter: %+v", list)
	}
	facets, err := inv.Facets(ctx)
	if err != nil || strings.Join(facets.Tags, ",") != "finance" || len(facets.TrustStates) != 2 {
		t.Fatalf("facets: %+v %v", facets, err)
	}
}

func TestEffectiveNameFollowsHostnameWithoutDiscoveredName(t *testing.T) {
	pool := testsupport.MigratedPool(t, "devices_namesync")
	ctx := context.Background()
	deviceID := testsupport.CreateDevice(t, pool, "old-name", "02:00:00:00:00:60", "")
	if _, err := pool.Exec(ctx, `UPDATE devices SET discovered_name = NULL WHERE entity_id = $1::uuid`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE devices SET hostname = 'new-name' WHERE entity_id = $1::uuid`, deviceID); err != nil {
		t.Fatal(err)
	}
	var name string
	_ = pool.QueryRow(ctx, `SELECT name FROM entities WHERE id = $1::uuid`, deviceID).Scan(&name)
	if name != "new-name" {
		t.Fatalf("entity name did not follow the hostname: %q", name)
	}
	// Multi-byte values are truncated on a character boundary, not mid-rune.
	tx, _ := pool.Begin(ctx)
	if err := devices.RecordAttribute(ctx, tx, deviceID, "operating_system", "test", strings.Repeat("ü", 300), 10); err != nil {
		t.Fatalf("long multi-byte value: %v", err)
	}
	_ = tx.Commit(ctx)
}
