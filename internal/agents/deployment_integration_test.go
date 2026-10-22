//go:build integration

package agents_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestDeploymentKeyLifecycle(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_deploy")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID, SourceAddress: "192.0.2.1"}
	// A device with the MAC that facts() reports, so association is observable.
	deviceID := testsupport.CreateDevice(t, pool, "office-pc", "02:00:00:00:00:01", "192.0.2.10")

	key, secret, err := repo.CreateDeploymentKey(ctx, agents.CreateDeploymentKeyInput{Name: "Office rollout", MaxUses: 2}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if key.State != "active" || key.Approval != agents.ApprovalManual || key.UsedCount != 0 ||
		!strings.HasPrefix(secret, agentproto.DeploymentKeyPrefix+"."+key.ID+".") {
		t.Fatalf("unexpected key %+v", key)
	}
	parsed, _ := agentproto.ParseEnrollmentToken(secret)
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT secret_hash FROM agent_deployment_keys WHERE id = $1::uuid`, key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if digest := sha256.Sum256(parsed.Secret); string(stored) != string(digest[:]) {
		t.Fatal("deployment key secret is not stored as a SHA-256 digest")
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE safe_parameters::text LIKE '%' || $1 || '%'`,
		base64.RawURLEncoding.EncodeToString(parsed.Secret)).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("deployment key secret leaked into the audit log (%d, %v)", leaked, err)
	}

	// Manual approval: the agent authenticates and heartbeats, but is pending.
	first, err := enroll(t, repo, secret, "office-pc")
	if err != nil || !first.result.Pending {
		t.Fatalf("first enrollment: %+v %v", first.result, err)
	}
	pending, _ := repo.Get(ctx, first.result.AgentID)
	if pending.Status != "pending" || pending.DeploymentKeyID != key.ID || pending.Device != nil || pending.Association.Status != "unassociated" {
		t.Fatalf("pending agent: %+v", pending)
	}
	principal, err := authenticate(repo, signed(t, first.result.CredentialID, first.key, "/agent/identity", []byte("{}"), time.Now()), []byte("{}"))
	if err != nil {
		t.Fatalf("pending agent cannot authenticate: %v", err)
	}
	if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("office-pc"), Capabilities: []string{agentproto.CapabilitySystemInfo}}); err != nil {
		t.Fatalf("pending heartbeat: %v", err)
	}
	if again, _ := repo.Get(ctx, first.result.AgentID); again.Device != nil || !again.Online {
		t.Fatalf("pending agent after heartbeat: device=%v online=%v", again.Device, again.Online)
	}
	commands := remote.NewService(pool)
	if _, err := commands.Create(ctx, remote.CreateInput{AgentID: first.result.AgentID, Type: agentproto.CapabilitySystemInfo}, actor); !errors.Is(err, remote.ErrAgentUnavailable) {
		t.Fatalf("command for a pending agent: %v", err)
	}
	if _, err := repo.UpdateAssociation(ctx, first.result.AgentID, agents.AssociationAction{Action: "confirm", DeviceID: deviceID}, actor); !errors.Is(err, agents.ErrAwaitingApproval) {
		t.Fatalf("association of a pending agent: %v", err)
	}

	// The use limit holds: the third enrollment is rejected and audited.
	second, err := enroll(t, repo, secret, "office-pc-2")
	if err != nil || !second.result.Pending || second.result.AgentID == first.result.AgentID {
		t.Fatalf("second enrollment: %+v %v", second.result, err)
	}
	if _, err := enroll(t, repo, secret, "office-pc-3"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("enrollment beyond the use limit: %v", err)
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT error_code FROM audit_log WHERE action = 'agent.enrollment.rejected' AND target_type = 'agent_deployment_key' AND target_id = $1`,
		key.ID).Scan(&reason); err != nil || reason != "use_limit_reached" {
		t.Fatalf("use limit audit: %q %v", reason, err)
	}
	if exhausted, _ := repo.GetDeploymentKey(ctx, key.ID); exhausted.UsedCount != 2 || exhausted.State != "exhausted" || exhausted.LastUsedAt == nil {
		t.Fatalf("exhausted key: %+v", exhausted)
	}

	// Approval activates and associates; it works once.
	approved, err := repo.Approve(ctx, first.result.AgentID, actor)
	if err != nil || approved.Status != "active" || approved.Device == nil || approved.Device.ID != deviceID {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := repo.Approve(ctx, first.result.AgentID, actor); !errors.Is(err, agents.ErrNotPendingApproval) {
		t.Fatalf("second approval: %v", err)
	}
	if auditCount(t, pool, "agent.approved", "success") != 1 {
		t.Fatal("approval was not audited")
	}
	if _, err := commands.Create(ctx, remote.CreateInput{AgentID: first.result.AgentID, Type: agentproto.CapabilitySystemInfo}, actor); err != nil {
		t.Fatalf("command after approval: %v", err)
	}

	// Rejecting is removal: the key stops working.
	if _, err := repo.Remove(ctx, second.result.AgentID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(repo, signed(t, second.result.CredentialID, second.key, "/agent/identity", []byte("{}"), time.Now()), []byte("{}")); err == nil {
		t.Fatal("a rejected agent still authenticates")
	}

	// Automatic approval activates immediately; revocation stops new enrollments only.
	automatic, autoSecret, err := repo.CreateDeploymentKey(ctx, agents.CreateDeploymentKeyInput{Name: "Lab", MaxUses: 5, Approval: agents.ApprovalAutomatic, TTL: 24 * time.Hour}, actor)
	if err != nil {
		t.Fatal(err)
	}
	lab, err := enroll(t, repo, autoSecret, "lab-1")
	if err != nil || lab.result.Pending {
		t.Fatalf("automatic enrollment: %+v %v", lab.result, err)
	}
	if active, _ := repo.Get(ctx, lab.result.AgentID); active.Status != "active" {
		t.Fatalf("automatically approved agent: %+v", active)
	}
	if _, err := repo.RevokeDeploymentKey(ctx, automatic.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevokeDeploymentKey(ctx, automatic.ID, actor); !errors.Is(err, agents.ErrDeploymentKeyNotActive) {
		t.Fatalf("second revocation: %v", err)
	}
	if _, err := enroll(t, repo, autoSecret, "lab-2"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("revoked key: %v", err)
	}
	if _, err := authenticate(repo, signed(t, lab.result.CredentialID, lab.key, "/agent/identity", []byte("{}"), time.Now()), []byte("{}")); err != nil {
		t.Fatalf("revoking the key affected an enrolled agent: %v", err)
	}

	// An expired key is rejected with its reason.
	expiredSecret := make([]byte, 32)
	expiredSecret[0] = 9
	expiredDigest := sha256.Sum256(expiredSecret)
	var expiredID string
	if err := pool.QueryRow(ctx, `INSERT INTO agent_deployment_keys (name, secret_hash, max_uses, created_by, created_at, expires_at)
		VALUES ('old', $1, 5, $2::uuid, now() - interval '2 days', now() - interval '1 day') RETURNING id::text`, expiredDigest[:], adminID).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	expiredKey := agentproto.FormatDeploymentKey(expiredID, base64.RawURLEncoding.EncodeToString(expiredSecret), testsupport.CoreFingerprint())
	if _, err := enroll(t, repo, expiredKey, "too-late"); !errors.Is(err, agents.ErrEnrollmentRejected) {
		t.Fatalf("expired key: %v", err)
	}

	// Input bounds.
	for name, input := range map[string]agents.CreateDeploymentKeyInput{
		"no name": {MaxUses: 1}, "no uses": {Name: "x"}, "too many uses": {Name: "x", MaxUses: 10001},
		"short TTL": {Name: "x", MaxUses: 1, TTL: 30 * time.Minute}, "long TTL": {Name: "x", MaxUses: 1, TTL: 91 * 24 * time.Hour},
		"bad approval": {Name: "x", MaxUses: 1, Approval: "maybe"},
	} {
		if _, _, err := repo.CreateDeploymentKey(ctx, input, actor); !errors.Is(err, agents.ErrInvalidInput) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}

	// PostgreSQL enforces the counters and terms on its own.
	for name, statement := range map[string]string{
		"decreasing use count": `UPDATE agent_deployment_keys SET used_count = 0 WHERE id = $1::uuid`,
		"use count over limit": `UPDATE agent_deployment_keys SET used_count = 3 WHERE id = $1::uuid`,
		"changed limit":        `UPDATE agent_deployment_keys SET max_uses = 100 WHERE id = $1::uuid`,
	} {
		if _, err := pool.Exec(ctx, statement, key.ID); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestConcurrentDeploymentKeyEnrollmentsRespectTheLimit(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_deploy_race")
	repo := testsupport.AgentRepository(pool)
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	key, secret, err := repo.CreateDeploymentKey(context.Background(), agents.CreateDeploymentKeyInput{Name: "race", MaxUses: 3}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	for attempt := 0; attempt < 10; attempt++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := enroll(t, repo, secret, "racer"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	final, _ := repo.GetDeploymentKey(context.Background(), key.ID)
	if successes != 3 || final.UsedCount != 3 {
		t.Fatalf("successes = %d, used = %d, want 3", successes, final.UsedCount)
	}
}
