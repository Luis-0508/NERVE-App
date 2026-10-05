//go:build integration

package agents_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func facts(hostname string) agentproto.HostFacts {
	return agentproto.HostFacts{Hostname: hostname, OS: "linux", Architecture: "amd64", AgentVersion: "test",
		Interfaces: []agentproto.InterfaceFact{{Name: "eth0", MAC: "02:00:00:00:00:01", Addresses: []string{"192.0.2.10", "127.0.0.1"}}}}
}

type enrolled struct {
	result agents.EnrollResult
	key    ed25519.PrivateKey
}

func enroll(t *testing.T, repo *agents.Repository, token, hostname string) (enrolled, error) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.Enroll(context.Background(), token, public, facts(hostname), "192.0.2.10")
	return enrolled{result: result, key: private}, err
}

func signed(t *testing.T, credentialID string, key ed25519.PrivateKey, route string, body []byte, now time.Time) agentproto.SignedHeaders {
	t.Helper()
	request := httptest.NewRequest("POST", "http://core/api/v1"+route, bytes.NewReader(body))
	if err := agentproto.Sign(request, credentialID, key, body, now); err != nil {
		t.Fatal(err)
	}
	headers, err := agentproto.ParseHeaders(request.Header)
	if err != nil {
		t.Fatal(err)
	}
	return headers
}

func authenticate(repo *agents.Repository, headers agentproto.SignedHeaders, body []byte) (agents.Principal, error) {
	return repo.Authenticate(context.Background(), headers, "POST", "/agent/identity", body, "192.0.2.10")
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action, result string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1 AND result = $2`, action, result).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestEnrollmentLifecycle(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID, SourceAddress: "192.0.2.1"}

	enrollment, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{DisplayName: "Lab server"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.State != "pending" || !strings.HasPrefix(token, agentproto.EnrollmentTokenPrefix+"."+enrollment.ID+".") {
		t.Fatalf("unexpected enrollment %+v / token prefix", enrollment)
	}
	// Only a digest of the secret is stored, never the token or the secret.
	parsedToken, _ := agentproto.ParseEnrollmentToken(token)
	secret := parsedToken.Secret
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT secret_hash FROM agent_enrollments WHERE id = $1::uuid`, enrollment.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(secret)
	if !bytes.Equal(stored, digest[:]) || bytes.Contains(stored, secret) {
		t.Fatal("enrollment secret is not stored as a SHA-256 digest")
	}
	var leaked int
	encodedSecret := base64.RawURLEncoding.EncodeToString(secret)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE safe_parameters::text LIKE '%' || $1 || '%'`, encodedSecret).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("enrollment secret leaked into the audit log (%d, %v)", leaked, err)
	}

	// Wrong secret for an existing enrollment is rejected and audited.
	forged := agentproto.FormatEnrollmentToken(enrollment.ID, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), testsupport.CoreFingerprint())
	if _, err := enroll(t, repo, forged, "attacker"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("forged secret: got %v", err)
	}
	if auditCount(t, pool, "agent.enrollment.rejected", "failure") != 1 {
		t.Fatal("forged enrollment attempt was not audited")
	}

	first, err := enroll(t, repo, token, "lab-server")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	agent, err := repo.Get(ctx, first.result.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.DisplayName != "Lab server" || agent.Status != "active" || agent.Credential == nil || agent.Credential.Fingerprint != first.result.Fingerprint {
		t.Fatalf("unexpected agent after enrollment: %+v", agent)
	}
	if len(agent.IPAddresses) != 1 || agent.IPAddresses[0] != "192.0.2.10" || len(agent.MACAddresses) != 1 {
		t.Fatalf("host facts were not normalized: %+v %+v", agent.IPAddresses, agent.MACAddresses)
	}
	var publicKey []byte
	if err := pool.QueryRow(ctx, `SELECT public_key FROM agent_credentials WHERE id = $1::uuid`, first.result.CredentialID).Scan(&publicKey); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicKey, first.key.Public().(ed25519.PublicKey)) {
		t.Fatal("stored credential is not the agent public key")
	}

	// Single use.
	if _, err := enroll(t, repo, token, "second-host"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("reused token: got %v", err)
	}
	if auditCount(t, pool, "agent.enrollment.completed", "success") != 1 || auditCount(t, pool, "agent.enrollment.created", "success") != 1 {
		t.Fatal("enrollment creation/completion audit records missing")
	}

	// Revoked enrollments cannot be used.
	revokedEnrollment, revokedToken, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevokeEnrollment(ctx, revokedEnrollment.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := enroll(t, repo, revokedToken, "late"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("revoked token: got %v", err)
	}
	if _, err := repo.RevokeEnrollment(ctx, revokedEnrollment.ID, actor); !errors.Is(err, agents.ErrEnrollmentNotActive) {
		t.Fatalf("second revocation: got %v", err)
	}

	// Expired enrollments cannot be used. Insert one whose lifetime already ended.
	expiredSecret := make([]byte, 32)
	expiredSecret[0] = 7
	expiredDigest := sha256.Sum256(expiredSecret)
	var expiredID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_enrollments (secret_hash, created_by, created_at, expires_at)
		VALUES ($1, $2::uuid, now() - interval '2 hours', now() - interval '1 hour') RETURNING id::text`, expiredDigest[:], adminID).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	expiredToken := agentproto.FormatEnrollmentToken(expiredID, base64.RawURLEncoding.EncodeToString(expiredSecret), testsupport.CoreFingerprint())
	if _, err := enroll(t, repo, expiredToken, "too-late"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("expired token: got %v", err)
	}
	var expiredReason string
	if err := pool.QueryRow(ctx, `SELECT error_code FROM audit_log WHERE action = 'agent.enrollment.rejected' AND target_id = $1`, expiredID).Scan(&expiredReason); err != nil || expiredReason != "expired" {
		t.Fatalf("expired attempt audit: %q %v", expiredReason, err)
	}

	// TTL bounds.
	for _, ttl := range []time.Duration{time.Minute, 25 * time.Hour} {
		if _, _, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{TTL: ttl}, actor); !errors.Is(err, agents.ErrInvalidInput) {
			t.Fatalf("TTL %s accepted: %v", ttl, err)
		}
	}
}

func TestConcurrentEnrollmentConsumesTokenOnce(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_race")
	repo := testsupport.AgentRepository(pool)
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	_, token, err := repo.CreateEnrollment(context.Background(), agents.CreateEnrollmentInput{}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for attempt := 0; attempt < 12; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := enroll(t, repo, token, "racer"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, agents.ErrEnrollmentRejected) {
				t.Errorf("unexpected enrollment error: %v", err)
			}
		}()
	}
	wg.Wait()
	var agentCount int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM agents`).Scan(&agentCount)
	if successes != 1 || agentCount != 1 {
		t.Fatalf("token consumed %d times, %d agents created", successes, agentCount)
	}
}

func TestSignedRequestAuthentication(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_auth")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID}
	newAgent := func(host string) enrolled {
		_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, actor)
		if err != nil {
			t.Fatal(err)
		}
		agent, err := enroll(t, repo, token, host)
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	a := newAgent("host-a")
	b := newAgent("host-b")
	body := []byte(`{}`)
	now := time.Now()

	principal, err := authenticate(repo, signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now), body)
	if err != nil || principal.AgentID != a.result.AgentID {
		t.Fatalf("valid request rejected: %+v %v", principal, err)
	}

	replayHeaders := signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now)
	if _, err := authenticate(repo, replayHeaders, body); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(repo, replayHeaders, body); !errors.Is(err, agents.ErrReplay) {
		t.Fatalf("replayed request: got %v", err)
	}
	if _, err := authenticate(repo, signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now.Add(-10*time.Minute)), body); !errors.Is(err, agentproto.ErrClockSkew) {
		t.Fatalf("stale timestamp: got %v", err)
	}
	tampered := signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now)
	if _, err := authenticate(repo, tampered, []byte(`{"x":1}`)); !errors.Is(err, agents.ErrAuthentication) {
		t.Fatalf("tampered body: got %v", err)
	}
	// Agent isolation: B's key cannot act under A's credential, and an unknown credential fails.
	if _, err := authenticate(repo, signed(t, a.result.CredentialID, b.key, "/agent/identity", body, now), body); !errors.Is(err, agents.ErrAuthentication) {
		t.Fatalf("cross-agent signature: got %v", err)
	}
	if _, err := authenticate(repo, signed(t, "00000000-0000-0000-0000-000000000000", a.key, "/agent/identity", body, now), body); !errors.Is(err, agents.ErrAuthentication) {
		t.Fatalf("unknown credential: got %v", err)
	}

	// Revocation affects only the revoked agent.
	if _, err := repo.Revoke(ctx, a.result.AgentID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(repo, signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now), body); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("revoked credential: got %v", err)
	}
	if _, err := authenticate(repo, signed(t, b.result.CredentialID, b.key, "/agent/identity", body, now), body); err != nil {
		t.Fatalf("revoking A affected B: %v", err)
	}
	if _, err := repo.Revoke(ctx, a.result.AgentID, actor); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("second revocation: got %v", err)
	}

	// Re-enrollment keeps the identity and issues a new credential.
	_, reToken, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{TargetAgentID: a.result.AgentID}, actor)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := enroll(t, repo, reToken, "host-a")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.result.AgentID != a.result.AgentID || renewed.result.CredentialID == a.result.CredentialID {
		t.Fatalf("re-enrollment did not keep the identity: %+v vs %+v", renewed.result, a.result)
	}
	if _, err := authenticate(repo, signed(t, renewed.result.CredentialID, renewed.key, "/agent/identity", body, now), body); err != nil {
		t.Fatalf("re-enrolled credential rejected: %v", err)
	}
	if _, err := authenticate(repo, signed(t, a.result.CredentialID, a.key, "/agent/identity", body, now), body); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("old credential still valid: %v", err)
	}

	// The same public key cannot be registered for a second agent.
	_, reuseToken, _ := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, actor)
	if _, err := repo.Enroll(ctx, reuseToken, b.key.Public().(ed25519.PublicKey), facts("copy"), ""); !errors.Is(err, agents.ErrKeyReuse) {
		t.Fatalf("key reuse: got %v", err)
	}

	// Removal is final.
	if _, err := repo.Remove(ctx, b.result.AgentID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(repo, signed(t, b.result.CredentialID, b.key, "/agent/identity", body, now), body); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("removed agent authenticated: %v", err)
	}
	if _, _, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{TargetAgentID: b.result.AgentID}, actor); !errors.Is(err, agents.ErrAgentRemoved) {
		t.Fatalf("re-enrolling removed agent: got %v", err)
	}
	for _, action := range []string{"agent.credential.revoked", "agent.removed"} {
		if auditCount(t, pool, action, "success") != 1 {
			t.Fatalf("missing audit record %s", action)
		}
	}

	if err := repo.PruneNonces(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialInvariantsAreEnforcedByPostgres(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_db")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	enrollmentRow, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := enroll(t, repo, token, "db-host")
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, _ := ed25519.GenerateKey(nil)
	for name, statement := range map[string]func() error{
		"replace public key": func() error {
			_, err := pool.Exec(ctx, `UPDATE agent_credentials SET public_key = $2 WHERE id = $1::uuid`, agent.result.CredentialID, []byte(otherPublic))
			return err
		},
		"delete credential": func() error {
			_, err := pool.Exec(ctx, `DELETE FROM agent_credentials WHERE id = $1::uuid`, agent.result.CredentialID)
			return err
		},
		"second active credential": func() error {
			_, err := pool.Exec(ctx, `INSERT INTO agent_credentials (agent_id, algorithm, public_key, fingerprint) VALUES ($1::uuid, 'ed25519', $2, $3)`,
				agent.result.AgentID, []byte(otherPublic), agentproto.Fingerprint(otherPublic))
			return err
		},
		"reuse enrollment": func() error {
			_, err := pool.Exec(ctx, `UPDATE agent_enrollments SET used_at = now() WHERE id = $1::uuid`, enrollmentRow.ID)
			return err
		},
		"extend enrollment": func() error {
			_, err := pool.Exec(ctx, `UPDATE agent_enrollments SET expires_at = expires_at + interval '1 hour' WHERE id = $1::uuid`, enrollmentRow.ID)
			return err
		},
		"edit audit": func() error {
			_, err := pool.Exec(ctx, `UPDATE audit_log SET result = 'failure'`)
			return err
		},
	} {
		if statement() == nil {
			t.Fatalf("%s was accepted by PostgreSQL", name)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_credentials SET revoked_at = now(), revocation_reason = 'revoked' WHERE id = $1::uuid`, agent.result.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_credentials SET revoked_at = NULL, revocation_reason = NULL WHERE id = $1::uuid`, agent.result.CredentialID); err == nil {
		t.Fatal("credential revocation could be undone")
	}
}

func TestEnrollmentDeviceAssociation(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_assoc")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID}
	deviceID := testsupport.CreateDevice(t, pool, "nas", "02:00:00:00:00:99", "192.0.2.50")

	_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{DeviceID: deviceID}, actor)
	if err != nil {
		t.Fatal(err)
	}
	first, err := enroll(t, repo, token, "nas")
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := repo.Get(ctx, first.result.AgentID)
	if agent.Device == nil || agent.Device.ID != deviceID || agent.Association.Status != "confirmed" {
		t.Fatalf("operator-selected device not associated: %+v", agent.Association)
	}
	var identityCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM device_identities WHERE identity_type = 'agent' AND value = $1 AND device_id = $2::uuid AND manually_confirmed_at IS NOT NULL`,
		first.result.AgentID, deviceID).Scan(&identityCount)
	if identityCount != 1 {
		t.Fatal("agent identity evidence was not recorded on the device")
	}

	// A second agent for the same device becomes a candidate instead of silently replacing the first.
	_, secondToken, _ := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{DeviceID: deviceID}, actor)
	second, err := enroll(t, repo, secondToken, "nas")
	if err != nil {
		t.Fatal(err)
	}
	secondAgent, _ := repo.Get(ctx, second.result.AgentID)
	if secondAgent.Device != nil || secondAgent.Association.Status != "candidate" || len(secondAgent.Association.Evidence) != 1 {
		t.Fatalf("conflicting association was not kept as a candidate: %+v", secondAgent.Association)
	}

	if _, err := repo.Remove(ctx, first.result.AgentID, actor); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM device_identities WHERE identity_type = 'agent' AND value = $1`, first.result.AgentID).Scan(&identityCount)
	if identityCount != 0 {
		t.Fatal("removed agent still listed as device identity")
	}
}
