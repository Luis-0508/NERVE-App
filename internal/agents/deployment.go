package agents

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/audit"
)

// CreateDeploymentKey stores a reusable deployment key and returns its secret
// once. Only the SHA-256 digest of the secret is persisted.
func (r *Repository) CreateDeploymentKey(ctx context.Context, input CreateDeploymentKeyInput, actor Actor) (DeploymentKey, string, error) {
	if r.coreKey == nil {
		return DeploymentKey{}, "", ErrCoreKeyMissing
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 200 {
		return DeploymentKey{}, "", fmt.Errorf("%w: a name of at most 200 characters is required", ErrInvalidInput)
	}
	if input.Approval == "" {
		input.Approval = ApprovalManual
	}
	if input.Approval != ApprovalManual && input.Approval != ApprovalAutomatic {
		return DeploymentKey{}, "", fmt.Errorf("%w: approval must be manual or automatic", ErrInvalidInput)
	}
	if input.MaxUses < 1 || input.MaxUses > MaxDeploymentKeyUses {
		return DeploymentKey{}, "", fmt.Errorf("%w: maximum uses must be between 1 and %d", ErrInvalidInput, MaxDeploymentKeyUses)
	}
	if input.TTL == 0 {
		input.TTL = DefaultDeploymentKeyTTL
	}
	if input.TTL < MinDeploymentKeyTTL || input.TTL > MaxDeploymentKeyTTL {
		return DeploymentKey{}, "", fmt.Errorf("%w: expiry must be between 1 hour and 90 days", ErrInvalidInput)
	}
	secret, err := agentproto.NewEnrollmentSecret()
	if err != nil {
		return DeploymentKey{}, "", err
	}
	rawSecret, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return DeploymentKey{}, "", fmt.Errorf("decode deployment key secret: %w", err)
	}
	digest := sha256.Sum256(rawSecret)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return DeploymentKey{}, "", fmt.Errorf("begin deployment key: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var keyID string
	if err := tx.QueryRow(ctx, `INSERT INTO agent_deployment_keys (name, secret_hash, approval, max_uses, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, now() + $6::interval) RETURNING id::text`,
		input.Name, digest[:], input.Approval, input.MaxUses, actor.UserID, fmt.Sprintf("%d seconds", int64(input.TTL.Seconds()))).Scan(&keyID); err != nil {
		return DeploymentKey{}, "", fmt.Errorf("create deployment key: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.deployment_key.created", TargetType: "agent_deployment_key",
		TargetID: keyID, SourceAddress: actor.SourceAddress, Parameters: map[string]any{
			"name": input.Name, "approval": input.Approval, "maxUses": input.MaxUses, "expiresInSeconds": int64(input.TTL.Seconds()),
		}}); err != nil {
		return DeploymentKey{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeploymentKey{}, "", fmt.Errorf("commit deployment key: %w", err)
	}
	key, err := r.GetDeploymentKey(ctx, keyID)
	if err != nil {
		return DeploymentKey{}, "", err
	}
	return key, agentproto.FormatDeploymentKey(keyID, secret, r.coreFingerprint()), nil
}

const deploymentKeyColumns = `k.id::text, k.name, k.approval, k.max_uses, k.used_count, k.created_by::text, COALESCE(u.email, ''),
	k.created_at, k.expires_at, k.last_used_at, k.revoked_at,
	CASE WHEN k.revoked_at IS NOT NULL THEN 'revoked' WHEN k.expires_at <= now() THEN 'expired'
	     WHEN k.used_count >= k.max_uses THEN 'exhausted' ELSE 'active' END`

func scanDeploymentKey(row pgx.Row) (DeploymentKey, error) {
	var k DeploymentKey
	err := row.Scan(&k.ID, &k.Name, &k.Approval, &k.MaxUses, &k.UsedCount, &k.CreatedBy, &k.CreatedByEmail,
		&k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt, &k.State)
	return k, err
}

func (r *Repository) GetDeploymentKey(ctx context.Context, id string) (DeploymentKey, error) {
	key, err := scanDeploymentKey(r.db.QueryRow(ctx, `SELECT `+deploymentKeyColumns+`
		FROM agent_deployment_keys k LEFT JOIN users u ON u.id = k.created_by WHERE k.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeploymentKey{}, ErrDeploymentKeyNotFound
	}
	if err != nil {
		return DeploymentKey{}, fmt.Errorf("read deployment key: %w", err)
	}
	return key, nil
}

func (r *Repository) ListDeploymentKeys(ctx context.Context, limit int) ([]DeploymentKey, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `SELECT `+deploymentKeyColumns+`
		FROM agent_deployment_keys k LEFT JOIN users u ON u.id = k.created_by ORDER BY k.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list deployment keys: %w", err)
	}
	defer rows.Close()
	items := make([]DeploymentKey, 0)
	for rows.Next() {
		key, err := scanDeploymentKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deployment key: %w", err)
		}
		items = append(items, key)
	}
	return items, rows.Err()
}

// RevokeDeploymentKey stops a key from enrolling further agents. Agents it
// already enrolled keep their own credentials.
func (r *Repository) RevokeDeploymentKey(ctx context.Context, id string, actor Actor) (DeploymentKey, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return DeploymentKey{}, fmt.Errorf("begin deployment key revocation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE agent_deployment_keys SET revoked_at = now(), revoked_by = $2::uuid
		WHERE id = $1::uuid AND revoked_at IS NULL AND expires_at > now()`, id, actor.UserID)
	if err != nil {
		return DeploymentKey{}, fmt.Errorf("revoke deployment key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetDeploymentKey(ctx, id); err != nil {
			return DeploymentKey{}, err
		}
		return DeploymentKey{}, ErrDeploymentKeyNotActive
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.deployment_key.revoked", TargetType: "agent_deployment_key",
		TargetID: id, SourceAddress: actor.SourceAddress}); err != nil {
		return DeploymentKey{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DeploymentKey{}, fmt.Errorf("commit deployment key revocation: %w", err)
	}
	return r.GetDeploymentKey(ctx, id)
}

// Approve activates an agent that a deployment key enrolled with manual
// approval. Only then can it receive commands and be linked to a device.
// Rejecting a pending agent is Remove.
func (r *Repository) Approve(ctx context.Context, agentID string, actor Actor) (Agent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Agent{}, fmt.Errorf("begin agent approval: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status, hostname, keyID string
	err = tx.QueryRow(ctx, `SELECT status, COALESCE(hostname, ''), COALESCE(deployment_key_id::text, '') FROM agents WHERE id = $1::uuid FOR UPDATE`,
		agentID).Scan(&status, &hostname, &keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("lock agent for approval: %w", err)
	}
	if status != "pending" {
		return Agent{}, ErrNotPendingApproval
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET status = 'active', updated_at = now() WHERE id = $1::uuid`, agentID); err != nil {
		return Agent{}, fmt.Errorf("approve agent: %w", err)
	}
	if err := evaluateAssociation(ctx, tx, agentID); err != nil {
		return Agent{}, err
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.approved", TargetType: "agent", TargetID: agentID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"deploymentKeyId": keyID, "hostname": hostname}}); err != nil {
		return Agent{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details)
		VALUES ((SELECT device_id FROM agents WHERE id = $1::uuid), 'agent.approved', 'info', 'remote-management',
		'A management agent was approved.', jsonb_build_object('agentId', $1::text, 'hostname', $2::text))`, agentID, hostname); err != nil {
		return Agent{}, fmt.Errorf("record approval event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Agent{}, fmt.Errorf("commit agent approval: %w", err)
	}
	return r.Get(ctx, agentID)
}
