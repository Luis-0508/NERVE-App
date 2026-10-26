//go:build integration

package agents_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCredentialRotation(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID, SourceAddress: "192.0.2.1"}
	enrollAgent := func(hostname string) enrolled {
		t.Helper()
		_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, actor)
		if err != nil {
			t.Fatal(err)
		}
		agent, err := enroll(t, repo, token, hostname)
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	principalOf := func(credentialID string, key ed25519.PrivateKey) (agents.Principal, error) {
		body := []byte("{}")
		return authenticate(repo, signed(t, credentialID, key, "/agent/identity", body, time.Now()), body)
	}
	rotate := func(principal agents.Principal, request agentproto.RotateCredentialRequest) (agents.RotationResult, error) {
		return repo.RotateCredential(ctx, principal, request, "192.0.2.10")
	}
	const proposedID = "8b0b6c1e-4f53-4c3a-9d55-0e7b0c9d1a01"

	first := enrollAgent("rotating-host")
	principal, err := principalOf(first.result.CredentialID, first.key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// Invalid proofs are refused before anything changes.
	otherAgent := agentproto.SignRotation("00000000-0000-4000-8000-000000000000", first.result.CredentialID, proposedID, newKey(t), now)
	stale := agentproto.SignRotation(first.result.AgentID, first.result.CredentialID, proposedID, newKey(t), now.Add(-agentproto.MaxClockSkew-time.Minute))
	sameKey := agentproto.SignRotation(first.result.AgentID, first.result.CredentialID, proposedID, first.key, now)
	for name, request := range map[string]agentproto.RotateCredentialRequest{"other agent": otherAgent, "stale": stale} {
		if _, err := rotate(principal, request); !errors.Is(err, agents.ErrInvalidRotation) {
			t.Fatalf("%s: got %v, want ErrInvalidRotation", name, err)
		}
	}
	if _, err := rotate(principal, sameKey); !errors.Is(err, agents.ErrKeyReuse) {
		t.Fatalf("rotating to the current key: got %v, want ErrKeyReuse", err)
	}

	// A valid rotation revokes the old credential as 'rotated' and activates the new one.
	nextKey := newKey(t)
	result, err := rotate(principal, agentproto.SignRotation(first.result.AgentID, first.result.CredentialID, proposedID, nextKey, now))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.CredentialID != proposedID || result.Fingerprint != agentproto.Fingerprint(nextKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("unexpected rotation result %+v", result)
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT revocation_reason FROM agent_credentials WHERE id = $1::uuid`, first.result.CredentialID).Scan(&reason); err != nil || reason != "rotated" {
		t.Fatalf("old credential reason = %q (%v), want rotated", reason, err)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_credentials WHERE agent_id = $1::uuid AND revoked_at IS NULL`, first.result.AgentID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active credentials = %d (%v), want 1", active, err)
	}
	if auditCount(t, pool, "agent.credential.rotated", "success") != 1 {
		t.Fatal("rotation was not audited")
	}
	agent, err := repo.Get(ctx, first.result.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "active" || agent.Credential == nil || agent.Credential.ID != proposedID || agent.Credential.Rotations != 1 || agent.Credential.LastRotatedAt == nil {
		t.Fatalf("unexpected agent after rotation: %+v / %+v", agent, agent.Credential)
	}

	// The old key no longer authenticates; the new one does.
	if _, err := principalOf(first.result.CredentialID, first.key); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("old key: got %v, want ErrCredentialRevoked", err)
	}
	rotated, err := principalOf(proposedID, nextKey)
	if err != nil || rotated.AgentID != first.result.AgentID {
		t.Fatalf("new key: %+v, %v", rotated, err)
	}
	// Replaying the rotation with the old principal finds no active credential.
	if _, err := rotate(principal, agentproto.SignRotation(first.result.AgentID, first.result.CredentialID,
		"8b0b6c1e-4f53-4c3a-9d55-0e7b0c9d1a02", newKey(t), time.Now())); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("replayed rotation: got %v, want ErrCredentialRevoked", err)
	}

	// Another agent can reuse neither the credential ID nor the key.
	second := enrollAgent("second-host")
	secondPrincipal, err := principalOf(second.result.CredentialID, second.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(secondPrincipal, agentproto.SignRotation(second.result.AgentID, second.result.CredentialID, proposedID, newKey(t), time.Now())); !errors.Is(err, agents.ErrCredentialIDInUse) {
		t.Fatalf("duplicate credential ID: got %v, want ErrCredentialIDInUse", err)
	}
	if _, err := rotate(secondPrincipal, agentproto.SignRotation(second.result.AgentID, second.result.CredentialID,
		"8b0b6c1e-4f53-4c3a-9d55-0e7b0c9d1a03", nextKey, time.Now())); !errors.Is(err, agents.ErrKeyReuse) {
		t.Fatalf("duplicate key: got %v, want ErrKeyReuse", err)
	}
	// A failed attempt leaves the caller's credential active.
	if _, err := principalOf(second.result.CredentialID, second.key); err != nil {
		t.Fatalf("second agent lost its credential after refused rotations: %v", err)
	}

	// An operator revocation that races a rotation wins.
	if _, err := repo.Revoke(ctx, second.result.AgentID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(secondPrincipal, agentproto.SignRotation(second.result.AgentID, second.result.CredentialID,
		"8b0b6c1e-4f53-4c3a-9d55-0e7b0c9d1a04", newKey(t), time.Now())); !errors.Is(err, agents.ErrCredentialRevoked) {
		t.Fatalf("revoked agent: got %v, want ErrCredentialRevoked", err)
	}

	// Agents still waiting for approval may rotate; they stay pending.
	_, deploySecret, err := repo.CreateDeploymentKey(ctx, agents.CreateDeploymentKeyInput{Name: "Rollout", MaxUses: 1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := enroll(t, repo, deploySecret, "waiting-host")
	if err != nil {
		t.Fatal(err)
	}
	waitingPrincipal, err := principalOf(waiting.result.CredentialID, waiting.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(waitingPrincipal, agentproto.SignRotation(waiting.result.AgentID, waiting.result.CredentialID,
		"8b0b6c1e-4f53-4c3a-9d55-0e7b0c9d1a05", newKey(t), time.Now())); err != nil {
		t.Fatalf("pending agent rotation: %v", err)
	}
	if pending, err := repo.Get(ctx, waiting.result.AgentID); err != nil || pending.Status != "pending" {
		t.Fatalf("pending agent after rotation: %+v, %v", pending.Status, err)
	}
}
