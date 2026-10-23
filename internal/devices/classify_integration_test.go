//go:build integration

package devices_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestHeuristicClassificationAndOverride(t *testing.T) {
	pool := testsupport.MigratedPool(t, "classify")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	service := devices.NewService(pool)
	repository := inventory.NewRepositories(pool).Devices

	// 00:1B:A9 is assigned to Brother Industries; 00:00:0C to Cisco, which has no rule.
	scan(t, pool, adminID,
		discovery.Observation{Address: "192.0.2.40", MAC: "00:1b:a9:00:00:40", Reachable: true, Method: "test"},
		discovery.Observation{Address: "192.0.2.41", MAC: "00:00:0c:00:00:41", Reachable: true, Method: "test"})
	printer := deviceByMAC(t, pool, "00:1b:a9:00:00:40")
	expect := func(deviceID, effective, discovered string, overridden bool) {
		t.Helper()
		device, err := repository.Get(ctx, deviceID)
		if err != nil {
			t.Fatal(err)
		}
		if device.Classification != effective || device.DiscoveredClassification != discovered || device.ClassificationOverridden != overridden {
			t.Fatalf("classification = %q (discovered %q, overridden %v), want %q (%q, %v)",
				device.Classification, device.DiscoveredClassification, device.ClassificationOverridden, effective, discovered, overridden)
		}
	}
	expect(printer, "printer", "printer", false)
	expect(deviceByMAC(t, pool, "00:00:0c:00:00:41"), "unknown", "unknown", false)

	report, err := devices.Identity(ctx, pool, printer)
	if err != nil {
		t.Fatal(err)
	}
	if report.Classification == nil || report.Classification.Classification != "printer" || report.Classification.Source != devices.SourceHeuristic || report.Classification.Reason == "" {
		t.Fatalf("identity report classification = %+v", report.Classification)
	}

	// The operator's override wins, and clearing it falls back to the heuristic.
	actor := devices.Actor{UserID: adminID}
	if err := service.UpdateMetadata(ctx, printer, devices.MetadataUpdate{Classification: strPtr("storage")}, actor); err != nil {
		t.Fatal(err)
	}
	expect(printer, "storage", "printer", true)
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.40", MAC: "00:1b:a9:00:00:40", Reachable: true, Method: "test"})
	expect(printer, "storage", "printer", true)
	if err := service.UpdateMetadata(ctx, printer, devices.MetadataUpdate{Classification: strPtr("")}, actor); err != nil {
		t.Fatal(err)
	}
	expect(printer, "printer", "printer", false)

	// A more confident rule takes over while its evidence lasts, and the
	// classification follows when that source's claims are forgotten.
	inTx := func(fn func(tx pgx.Tx) error) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, pool, fn); err != nil {
			t.Fatal(err)
		}
	}
	inTx(func(tx pgx.Tx) error {
		return devices.RecordAttribute(ctx, tx, printer, "operating_system", devices.SourceAgent, "Windows Server 2022 Standard", devices.ConfidenceAgent)
	})
	expect(printer, "server", "server", false)
	inTx(func(tx pgx.Tx) error { return devices.ForgetSource(ctx, tx, printer, devices.SourceAgent) })
	expect(printer, "printer", "printer", false)

	// With no matching evidence left the heuristic claim is withdrawn.
	inTx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM device_attributes WHERE device_id = $1::uuid AND attribute = 'manufacturer'`, printer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE devices SET manufacturer = NULL WHERE entity_id = $1::uuid`, printer); err != nil {
			return err
		}
		return devices.RecordClassification(ctx, tx, printer)
	})
	expect(printer, "unknown", "unknown", false)

	// The startup pass restores the verdict for existing devices and is repeatable.
	inTx(func(tx pgx.Tx) error {
		return devices.RecordAttribute(ctx, tx, printer, "manufacturer", devices.SourceOUI, "Brother Industries, LTD.", devices.ConfidenceOUI)
	})
	if _, err := pool.Exec(ctx, `DELETE FROM device_attributes WHERE attribute = 'classification'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := devices.BackfillClassifications(ctx, pool); err != nil {
			t.Fatal(err)
		}
		expect(printer, "printer", "printer", false)
	}
}
