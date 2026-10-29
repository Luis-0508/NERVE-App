//go:build integration

package discovery_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/platform/database"
)

func TestDiscoveryHardeningInvariants(t *testing.T) {
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

	schema := fmt.Sprintf("nerve_hardening_it_%d", time.Now().UnixNano())
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

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
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
		VALUES ('hardening-integration@example.invalid', 'not-a-login-password') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("create integration user: %v", err)
	}

	repository := discovery.NewRepository(pool)
	target, err := repository.SaveTarget(ctx, "Hardening network", "192.168.178.0/24", userID, "")
	if err != nil {
		t.Fatalf("save discovery target: %v", err)
	}
	const agentID = "hardening-agent"
	if err := repository.Heartbeat(ctx, discovery.LegacyWorker(agentID), discovery.AgentHeartbeat{
		ID: agentID, Hostname: "hardening-host", Platform: "test", Version: "test",
		Capabilities: []string{"integration-report"},
	}); err != nil {
		t.Fatalf("register discovery agent: %v", err)
	}

	// Competing API requests may all pass the optimistic pre-check, but the
	// database invariant must still allow exactly one active scan.
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	var workers sync.WaitGroup
	for i := 0; i < contenders; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := repository.Queue(ctx, target.ID, userID, "")
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	rejected := 0
	for queueErr := range results {
		switch {
		case queueErr == nil:
			succeeded++
		case errors.Is(queueErr, discovery.ErrScanActive):
			rejected++
		default:
			t.Fatalf("unexpected concurrent queue error: %v", queueErr)
		}
	}
	if succeeded != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent queue results: succeeded=%d rejected=%d, want 1/%d", succeeded, rejected, contenders-1)
	}

	job, err := repository.Claim(ctx, discovery.LegacyWorker(agentID))
	if err != nil {
		t.Fatalf("claim concurrency-test scan: %v", err)
	}
	if err := repository.Complete(ctx, job.ID, discovery.LegacyWorker(agentID), discovery.Completion{Status: "failed", ErrorMessage: "intentional integration cleanup"}); err != nil {
		t.Fatalf("complete concurrency-test scan: %v", err)
	}

	observation := discovery.Observation{
		Address: "192.168.178.50", MAC: "aa:bb:cc:dd:ee:ff", Hostname: "hardening-device",
		Reachable: true, Method: "integration-test",
	}
	runHardeningScan(t, ctx, repository, target.ID, userID, agentID, []discovery.Observation{observation})
	assertDiscoveryChildState(t, ctx, pool, "online", true)

	// The same MAC moving to a new address must keep the device/interface online
	// while retiring the old IP rather than leaving two active address entities.
	movedObservation := observation
	movedObservation.Address = "192.168.178.51"
	time.Sleep(25 * time.Millisecond)
	runHardeningScan(t, ctx, repository, target.ID, userID, agentID, []discovery.Observation{movedObservation})
	assertDiscoveryChildState(t, ctx, pool, "online", true)
	assertAddressRetired(t, ctx, pool, observation.Address)

	time.Sleep(25 * time.Millisecond)
	runHardeningScan(t, ctx, repository, target.ID, userID, agentID, nil)
	assertDiscoveryChildState(t, ctx, pool, "offline", false)

	// A later observation must revive the same device/interface/current-IP entities.
	time.Sleep(25 * time.Millisecond)
	runHardeningScan(t, ctx, repository, target.ID, userID, agentID, []discovery.Observation{movedObservation})
	assertDiscoveryChildState(t, ctx, pool, "online", true)
}

func runHardeningScan(t *testing.T, ctx context.Context, repository *discovery.Repository, targetID, userID, agentID string, observations []discovery.Observation) {
	t.Helper()
	scan, err := repository.Queue(ctx, targetID, userID, "")
	if err != nil {
		t.Fatalf("queue discovery scan: %v", err)
	}
	job, err := repository.Claim(ctx, discovery.LegacyWorker(agentID))
	if err != nil {
		t.Fatalf("claim discovery scan: %v", err)
	}
	if job.ID != scan.ID {
		t.Fatalf("claimed scan %s, want %s", job.ID, scan.ID)
	}
	if err := repository.Report(ctx, job.ID, discovery.LegacyWorker(agentID), discovery.Report{Observations: observations}); err != nil {
		t.Fatalf("report discovery observations: %v", err)
	}
	if err := repository.Complete(ctx, job.ID, discovery.LegacyWorker(agentID), discovery.Completion{Status: "succeeded", AddressesTotal: 254}); err != nil {
		t.Fatalf("complete discovery scan: %v", err)
	}
}

func assertDiscoveryChildState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantStatus string, wantUp bool) {
	t.Helper()
	var deviceStatus, interfaceStatus, ipStatus string
	var isUp bool
	var validUntil sql.NullTime
	if err := pool.QueryRow(ctx, `SELECT device.status, interface_entity.status, ip_entity.status, interface.is_up, ip.valid_until
		FROM entities device
		JOIN devices d ON d.entity_id = device.id
		JOIN network_interfaces interface ON interface.device_id = d.entity_id
		JOIN entities interface_entity ON interface_entity.id = interface.entity_id
		JOIN interface_addresses assignment ON assignment.interface_id = interface.entity_id
		JOIN ip_addresses ip ON ip.entity_id = assignment.ip_address_id
		JOIN entities ip_entity ON ip_entity.id = ip.entity_id
		WHERE device.kind = 'device'
		ORDER BY device.created_at, ip.valid_from DESC
		LIMIT 1`).Scan(&deviceStatus, &interfaceStatus, &ipStatus, &isUp, &validUntil); err != nil {
		t.Fatalf("read discovery child state: %v", err)
	}
	if deviceStatus != wantStatus || interfaceStatus != wantStatus || ipStatus != wantStatus || isUp != wantUp {
		t.Fatalf("state device=%s interface=%s ip=%s is_up=%v, want %s/%s/%s/%v", deviceStatus, interfaceStatus, ipStatus, isUp, wantStatus, wantStatus, wantStatus, wantUp)
	}
	if wantStatus == "online" && validUntil.Valid {
		t.Fatal("online discovery address is unexpectedly retired")
	}
	if wantStatus == "offline" && !validUntil.Valid {
		t.Fatal("offline discovery address is unexpectedly still active")
	}
}

func assertAddressRetired(t *testing.T, ctx context.Context, pool *pgxpool.Pool, address string) {
	t.Helper()
	var status string
	var validUntil sql.NullTime
	if err := pool.QueryRow(ctx, `SELECT entity.status, ip.valid_until
		FROM ip_addresses ip
		JOIN entities entity ON entity.id = ip.entity_id
		WHERE host(ip.address) = $1
		ORDER BY ip.valid_from
		LIMIT 1`, address).Scan(&status, &validUntil); err != nil {
		t.Fatalf("read retired address %s: %v", address, err)
	}
	if status != "offline" || !validUntil.Valid {
		t.Fatalf("retired address %s status=%s valid_until=%v, want offline with valid_until", address, status, validUntil.Valid)
	}
}
