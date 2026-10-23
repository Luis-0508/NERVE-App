//go:build integration

package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestDiscoveredDeviceGainsVendorFromOUI(t *testing.T) {
	pool := testsupport.MigratedPool(t, "vendor")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash)
		VALUES ('vendor-integration@example.invalid', 'not-a-login-password') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("create integration user: %v", err)
	}
	repository := discovery.NewRepository(pool)
	inventoryRepositories := inventory.NewRepositories(pool)
	target, err := repository.SaveTarget(ctx, "Vendor network", "192.168.178.0/24", userID, "")
	if err != nil {
		t.Fatalf("save discovery target: %v", err)
	}
	const agentID = "vendor-agent"
	if err := repository.Heartbeat(ctx, discovery.AgentHeartbeat{ID: agentID, Hostname: "vendor-host", Platform: "test", Version: "test", Capabilities: []string{"integration-report"}}); err != nil {
		t.Fatalf("register discovery agent: %v", err)
	}
	observation := discovery.Observation{Address: "192.168.178.60", MAC: "00:15:5d:12:34:56", Reachable: true, Method: "integration-test"}
	scan := func() inventory.DeviceSummary {
		t.Helper()
		runSuccessfulScan(t, ctx, repository, target.ID, userID, agentID, []discovery.Observation{observation})
		return requireSingleDevice(t, ctx, inventoryRepositories)
	}

	device := scan()
	if device.Vendor != "Microsoft Corporation" {
		t.Fatalf("vendor = %q, want the OUI registry's Microsoft Corporation", device.Vendor)
	}
	if source := effectiveSource(t, ctx, pool, device.ID); source != devices.SourceOUI {
		t.Fatalf("effective manufacturer source = %q, want %q", source, devices.SourceOUI)
	}

	// An enrolled agent describing its own hardware outranks the registry,
	// and later scans do not take that back.
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return devices.RecordAttribute(ctx, tx, device.ID, "manufacturer", devices.SourceAgent, "Contoso Hardware", devices.ConfidenceAgent)
	}); err != nil {
		t.Fatalf("record agent manufacturer: %v", err)
	}
	if device = scan(); device.Vendor != "Contoso Hardware" {
		t.Fatalf("vendor after agent claim = %q, want Contoso Hardware", device.Vendor)
	}
	if source := effectiveSource(t, ctx, pool, device.ID); source != devices.SourceAgent {
		t.Fatalf("effective manufacturer source = %q, want %q", source, devices.SourceAgent)
	}

	// Devices recorded before the lookup existed are backfilled once.
	if _, err := pool.Exec(ctx, `DELETE FROM device_attributes WHERE source = $1`, devices.SourceOUI); err != nil {
		t.Fatalf("remove OUI claims: %v", err)
	}
	for run, want := range []int{1, 0} {
		recorded, err := devices.BackfillVendors(ctx, pool)
		if err != nil || recorded != want {
			t.Fatalf("backfill run %d recorded %d (err %v), want %d", run+1, recorded, err, want)
		}
	}
	var vendor string
	if err := pool.QueryRow(ctx, `SELECT value FROM device_attributes WHERE device_id = $1::uuid AND attribute = 'manufacturer' AND source = $2`,
		device.ID, devices.SourceOUI).Scan(&vendor); err != nil || vendor != "Microsoft Corporation" {
		t.Fatalf("backfilled OUI claim = %q (err %v)", vendor, err)
	}

	// A randomized (locally administered) MAC names no vendor.
	observation = discovery.Observation{Address: "192.168.178.61", MAC: "da:a1:19:00:00:01", Reachable: true, Method: "integration-test"}
	runSuccessfulScan(t, ctx, repository, target.ID, userID, agentID, []discovery.Observation{observation})
	var claims int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_attributes attribute
		JOIN device_identities identity ON identity.device_id = attribute.device_id
		WHERE identity.identity_type = 'mac' AND identity.value = 'da:a1:19:00:00:01' AND attribute.source = $1`, devices.SourceOUI).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("randomized MAC produced %d OUI claims (err %v)", claims, err)
	}
}

func effectiveSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deviceID string) string {
	t.Helper()
	attributes, err := devices.ListAttributes(ctx, pool, deviceID)
	if err != nil {
		t.Fatalf("list device attributes: %v", err)
	}
	for _, attribute := range attributes {
		if attribute.Attribute == "manufacturer" && attribute.Effective {
			return attribute.Source
		}
	}
	return ""
}
