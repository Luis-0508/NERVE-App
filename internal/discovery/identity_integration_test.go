//go:build integration

package discovery_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// TestDiscoveryIdentityMigrationKeepsLegacyHistory replays migration 000015 on
// a database that already holds a legacy worker and its scans.
func TestDiscoveryIdentityMigrationKeepsLegacyHistory(t *testing.T) {
	pool := testsupport.MigratedPool(t, "discovery_identity")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, currentFile, _, _ := runtime.Caller(0)
	migration := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if _, err := pool.Exec(ctx, migration("000015_discovery_agent_identity.down.sql")); err != nil {
		t.Fatalf("roll back 000015: %v", err)
	}

	userID := testsupport.CreateUser(t, pool, "legacy@example.invalid", "integration-password", "administrator")
	if _, err := pool.Exec(ctx, `INSERT INTO discovery_agents (id, hostname, platform, version) VALUES ('legacy-host-linux', 'legacy-host', 'linux/amd64', '0.1.0')`); err != nil {
		t.Fatalf("insert legacy worker: %v", err)
	}
	var scanID string
	if err := pool.QueryRow(ctx, `WITH target AS (
			INSERT INTO discovery_targets (name, cidr, created_by) VALUES ('Legacy', '192.0.2.0/24', $1::uuid) RETURNING id)
		INSERT INTO discovery_scans (target_id, requested_by, agent_id, status, started_at, completed_at)
		SELECT id, $1::uuid, 'legacy-host-linux', 'succeeded', now(), now() FROM target RETURNING id::text`, userID).Scan(&scanID); err != nil {
		t.Fatalf("insert legacy scan: %v", err)
	}

	if _, err := pool.Exec(ctx, migration("000015_discovery_agent_identity.up.sql")); err != nil {
		t.Fatalf("apply 000015 over legacy rows: %v", err)
	}
	state, err := discovery.NewRepository(pool).State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Agent == nil || state.Agent.ID != "legacy-host-linux" || !state.Agent.LegacyToken || state.Agent.AgentID != "" {
		t.Fatalf("legacy worker after migration: %+v", state.Agent)
	}
	if state.Scan == nil || state.Scan.ID != scanID || state.Scan.AgentID != "legacy-host-linux" || state.Scan.Status != "succeeded" {
		t.Fatalf("legacy scan history after migration: %+v", state.Scan)
	}
	// The legacy worker keeps working on the migrated schema.
	if err := discovery.NewRepository(pool).Heartbeat(ctx, discovery.LegacyWorker("legacy-host-linux"),
		discovery.AgentHeartbeat{Hostname: "legacy-host", Platform: "linux/amd64", Version: "0.1.0"}); err != nil {
		t.Fatalf("legacy heartbeat after migration: %v", err)
	}
}
