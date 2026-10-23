//go:build integration

package inventory_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestDeviceListIncludesCurrentAddresses(t *testing.T) {
	pool := testsupport.MigratedPool(t, "inventory")
	ctx := context.Background()
	deviceID := testsupport.CreateDevice(t, pool, "multi-homed", "00:11:22:33:44:55", "192.0.2.10")
	var interfaceID string
	if err := pool.QueryRow(ctx, `SELECT entity_id::text FROM network_interfaces WHERE device_id = $1::uuid`, deviceID).Scan(&interfaceID); err != nil {
		t.Fatal(err)
	}
	// A current secondary address, an ended lease and a duplicate row of the primary.
	addAddress(t, pool, interfaceID, "198.51.100.7", "now() + interval '1 minute'", "NULL")
	addAddress(t, pool, interfaceID, "203.0.113.9", "now() - interval '2 days'", "now() - interval '1 day'")
	addAddress(t, pool, interfaceID, "192.0.2.10", "now() - interval '1 hour'", "NULL")

	repository := inventory.NewRepositories(pool).Devices
	list := func() []string {
		t.Helper()
		result, err := repository.List(ctx, inventory.DeviceListParams{Sort: "name", Direction: "asc", Limit: 10})
		if err != nil || len(result.Items) != 1 {
			t.Fatalf("List() = %+v, %v", result, err)
		}
		return result.Items[0].IPAddresses
	}
	if got := fmt.Sprint(list()); got != "[192.0.2.10 198.51.100.7]" {
		t.Fatalf("current addresses = %s, want the primary first, the secondary, no ended lease and no duplicate", got)
	}

	for i := range 20 {
		addAddress(t, pool, interfaceID, fmt.Sprintf("10.20.0.%d", i+1), "now()", "NULL")
	}
	if got := list(); len(got) != 16 || got[0] != "192.0.2.10" {
		t.Fatalf("bounded addresses = %v, want 16 starting with the primary", got)
	}
	device, err := repository.Get(ctx, deviceID)
	if err != nil || len(device.IPAddresses) != 16 {
		t.Fatalf("Get() addresses = %v, %v", device.IPAddresses, err)
	}
}

func addAddress(t *testing.T, pool *pgxpool.Pool, interfaceID, address, validFrom, validUntil string) {
	t.Helper()
	ctx := context.Background()
	var ipID string
	if err := pool.QueryRow(ctx, `INSERT INTO entities (kind, name, status) VALUES ('ip_address', $1, 'online') RETURNING id::text`, address).Scan(&ipID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ip_addresses (entity_id, address, valid_from, valid_until)
		VALUES ($1::uuid, $2::inet, `+validFrom+`, `+validUntil+`)`, ipID, address); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO interface_addresses (interface_id, ip_address_id) VALUES ($1::uuid, $2::uuid)`, interfaceID, ipID); err != nil {
		t.Fatal(err)
	}
}
