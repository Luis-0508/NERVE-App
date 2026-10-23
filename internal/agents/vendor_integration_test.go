//go:build integration

package agents_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestAssociatedAgentMACRecordsOUIVendor(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_vendor")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	device := testsupport.CreateDevice(t, pool, "hyperv-guest", "00:15:5d:00:00:30", "192.0.2.30")

	_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	if _, err := repo.Enroll(ctx, token, public, hostFacts("hyperv-guest", "00:15:5d:00:00:30", "192.0.2.30"), ""); err != nil {
		t.Fatal(err)
	}
	var vendor string
	if err := pool.QueryRow(ctx, `SELECT value FROM device_attributes WHERE device_id = $1::uuid AND attribute = 'manufacturer' AND source = $2`,
		device, devices.SourceOUI).Scan(&vendor); err != nil || vendor != "Microsoft Corporation" {
		t.Fatalf("OUI vendor for the agent's MAC = %q (err %v), want Microsoft Corporation", vendor, err)
	}
}
