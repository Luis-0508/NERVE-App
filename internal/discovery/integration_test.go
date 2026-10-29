//go:build integration

package discovery_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/platform/database"
)

func TestDiscoveryInventoryFlow(t *testing.T) {
	databaseURL := os.Getenv("NERVE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NERVE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := fmt.Sprintf("nerve_discovery_it_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated integration schema: %v", err)
	}
	defer pool.Close()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations"))
	if err := database.Migrate(ctx, pool, migrationsDir); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}

	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash)
		VALUES ('discovery-integration@example.invalid', 'not-a-login-password') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("create integration user: %v", err)
	}

	discoveryRepository := discovery.NewRepository(pool)
	inventoryRepositories := inventory.NewRepositories(pool)
	target, err := discoveryRepository.SaveTarget(ctx, "Integration network", "192.168.178.0/24", userID, "")
	if err != nil {
		t.Fatalf("save discovery target: %v", err)
	}
	const agentID = "integration-agent"
	if err := discoveryRepository.Heartbeat(ctx, discovery.LegacyWorker(agentID), discovery.AgentHeartbeat{
		ID: agentID, Hostname: "integration-host", Platform: "test", Version: "test",
		Capabilities: []string{"integration-report"},
	}); err != nil {
		t.Fatalf("register discovery agent: %v", err)
	}

	observation := discovery.Observation{
		Address: "192.168.178.50", MAC: "aa:bb:cc:dd:ee:ff", Hostname: "test-device",
		Reachable: true, Method: "integration-test",
	}
	runSuccessfulScan(t, ctx, discoveryRepository, target.ID, userID, agentID, []discovery.Observation{observation})
	first := requireSingleDevice(t, ctx, inventoryRepositories)
	if first.Status != "online" || first.Hostname != "test-device" || first.PrimaryIPAddress != "192.168.178.50" || first.PrimaryMAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected discovered device: %+v", first)
	}

	time.Sleep(25 * time.Millisecond)
	runSuccessfulScan(t, ctx, discoveryRepository, target.ID, userID, agentID, []discovery.Observation{observation})
	second := requireSingleDevice(t, ctx, inventoryRepositories)
	if second.ID != first.ID {
		t.Fatalf("repeat scan created a new device: first=%s second=%s", first.ID, second.ID)
	}
	if !second.FirstSeenAt.Equal(first.FirstSeenAt) {
		t.Fatalf("first_seen changed: first=%s second=%s", first.FirstSeenAt, second.FirstSeenAt)
	}
	if !second.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("last_seen did not advance: first=%s second=%s", first.LastSeenAt, second.LastSeenAt)
	}

	time.Sleep(25 * time.Millisecond)
	runSuccessfulScan(t, ctx, discoveryRepository, target.ID, userID, agentID, nil)
	offline := requireSingleDevice(t, ctx, inventoryRepositories)
	if offline.ID != first.ID || offline.Status != "offline" {
		t.Fatalf("unseen device was not marked offline: %+v", offline)
	}
}

func runSuccessfulScan(t *testing.T, ctx context.Context, repository *discovery.Repository, targetID, userID, agentID string, observations []discovery.Observation) {
	t.Helper()
	scan, err := repository.Queue(ctx, targetID, userID, "")
	if err != nil {
		t.Fatalf("queue discovery scan: %v", err)
	}
	job, err := repository.Claim(ctx, discovery.LegacyWorker(agentID))
	if err != nil {
		t.Fatalf("claim discovery scan: %v", err)
	}
	if job.ID != scan.ID || job.CIDR != "192.168.178.0/24" {
		t.Fatalf("unexpected claimed job: %+v", job)
	}
	if err := repository.Report(ctx, job.ID, discovery.LegacyWorker(agentID), discovery.Report{Observations: observations}); err != nil {
		t.Fatalf("report discovery observations: %v", err)
	}
	if err := repository.Complete(ctx, job.ID, discovery.LegacyWorker(agentID), discovery.Completion{Status: "succeeded", AddressesTotal: 254}); err != nil {
		t.Fatalf("complete discovery scan: %v", err)
	}
}

func requireSingleDevice(t *testing.T, ctx context.Context, repositories *inventory.Repositories) inventory.DeviceSummary {
	t.Helper()
	result, err := repositories.Devices.List(ctx, inventory.DeviceListParams{Sort: "name", Direction: "asc", Limit: 50})
	if err != nil {
		t.Fatalf("list discovered devices: %v", err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("got %d devices (%d returned), want exactly one", result.Total, len(result.Items))
	}
	return result.Items[0]
}
