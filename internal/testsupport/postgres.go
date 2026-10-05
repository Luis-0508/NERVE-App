//go:build integration

// Package testsupport provides isolated, migrated PostgreSQL schemas for
// integration tests. It is compiled only with the integration build tag.
package testsupport

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/platform/database"
)

// MigratedPool returns a pool bound to a fresh schema with all migrations
// applied. The schema is dropped when the test ends.
func MigratedPool(t *testing.T, prefix string) *pgxpool.Pool {
	t.Helper()
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
	schema := fmt.Sprintf("nerve_%s_it_%d", prefix, time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		adminPool.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated integration schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
		adminPool.Close()
	})
	_, currentFile, _, _ := runtime.Caller(0)
	migrations := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations"))
	if err := database.Migrate(ctx, pool, migrations); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}
	return pool
}

// CreateUser inserts a user with the given role and returns its ID.
func CreateUser(t *testing.T, pool *pgxpool.Pool, email, password, role string) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id::text`, email, hash).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1::uuid, id FROM roles WHERE key = $2`, id, role); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	return id
}

// CreateDevice inserts a minimal device with optional MAC and IP evidence.
func CreateDevice(t *testing.T, pool *pgxpool.Pool, name, mac, address string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO entities (kind, name, status) VALUES ('device', $1, 'online') RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatalf("create device entity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO devices (entity_id, hostname, discovered_name) VALUES ($1::uuid, $2, $2)`, id, name); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence) VALUES ($1::uuid, 'hostname', lower($2), 'test', 65)`, id, name); err != nil {
		t.Fatalf("create hostname identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_attributes (device_id, attribute, source, value, confidence) VALUES ($1::uuid, 'hostname', 'test', $2, 50)`, id, name); err != nil {
		t.Fatalf("create hostname fact: %v", err)
	}
	var interfaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO entities (kind, name, status) VALUES ('network_interface', 'eth0', 'online') RETURNING id::text`).Scan(&interfaceID); err != nil {
		t.Fatalf("create interface entity: %v", err)
	}
	var macValue any
	if mac != "" {
		macValue = mac
		if _, err := pool.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence) VALUES ($1::uuid, 'mac', $2, 'test', 90)`, id, mac); err != nil {
			t.Fatalf("create MAC identity: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO network_interfaces (entity_id, device_id, name, mac_address, is_up) VALUES ($1::uuid, $2::uuid, 'eth0', $3::macaddr, true)`, interfaceID, id, macValue); err != nil {
		t.Fatalf("create interface: %v", err)
	}
	if address != "" {
		var ipID string
		if err := pool.QueryRow(ctx, `INSERT INTO entities (kind, name, status) VALUES ('ip_address', $1, 'online') RETURNING id::text`, address).Scan(&ipID); err != nil {
			t.Fatalf("create IP entity: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO ip_addresses (entity_id, address, is_primary) VALUES ($1::uuid, $2::inet, true)`, ipID, address); err != nil {
			t.Fatalf("create IP: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO interface_addresses (interface_id, ip_address_id) VALUES ($1::uuid, $2::uuid)`, interfaceID, ipID); err != nil {
			t.Fatalf("attach IP: %v", err)
		}
	}
	return id
}

// CoreSigningKeyHex is a fixed test-only NERVE_CORE_SIGNING_KEY.
const CoreSigningKeyHex = "4e45525645207465737420636f7265207369676e696e67206b65792030303031"

// CoreKey returns the test Core signing key.
func CoreKey() ed25519.PrivateKey {
	key, err := agentproto.ParseCoreSigningKey(CoreSigningKeyHex)
	if err != nil {
		panic(err)
	}
	return key
}

// AgentRepository returns an agents repository configured with the test Core key.
func AgentRepository(pool *pgxpool.Pool) *agents.Repository {
	repository := agents.NewRepository(pool)
	repository.SetCoreSigningKey(CoreKey())
	return repository
}

// CoreFingerprint is the fingerprint embedded in test enrollment tokens.
func CoreFingerprint() string {
	return agentproto.CoreFingerprint(CoreKey().Public().(ed25519.PublicKey))
}
