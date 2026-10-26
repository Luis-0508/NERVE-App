package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/audit"
)

// RotationResult describes the credential that replaced the caller's.
type RotationResult struct {
	CredentialID string
	Fingerprint  string
	CreatedAt    time.Time
}

// RotateCredential replaces the authenticated credential with the key in
// request. The request was signed with the old key (principal); the proof
// shows possession of the new one. The old credential is revoked as 'rotated'
// in the same transaction, so exactly one credential stays active.
func (r *Repository) RotateCredential(ctx context.Context, principal Principal, request agentproto.RotateCredentialRequest, sourceAddress string) (RotationResult, error) {
	publicKey, err := agentproto.VerifyRotation(request, principal.AgentID, principal.CredentialID, r.now())
	if err != nil {
		return RotationResult{}, ErrInvalidRotation
	}
	fingerprint := agentproto.Fingerprint(publicKey)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return RotationResult{}, fmt.Errorf("begin credential rotation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM agents WHERE id = $1::uuid FOR UPDATE`, principal.AgentID).Scan(&status); err != nil {
		return RotationResult{}, fmt.Errorf("lock rotating agent: %w", err)
	}
	if status != "active" && status != "pending" {
		return RotationResult{}, ErrCredentialRevoked
	}
	var oldFingerprint string
	err = tx.QueryRow(ctx, `UPDATE agent_credentials SET revoked_at = now(), revocation_reason = 'rotated'
		WHERE id = $1::uuid AND agent_id = $2::uuid AND revoked_at IS NULL RETURNING fingerprint`,
		principal.CredentialID, principal.AgentID).Scan(&oldFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent rotation or an operator revocation won the race.
		return RotationResult{}, ErrCredentialRevoked
	}
	if err != nil {
		return RotationResult{}, fmt.Errorf("revoke rotated credential: %w", err)
	}
	var result RotationResult
	err = tx.QueryRow(ctx, `INSERT INTO agent_credentials (id, agent_id, algorithm, public_key, fingerprint)
		VALUES ($1::uuid, $2::uuid, 'ed25519', $3, $4) RETURNING id::text, fingerprint, created_at`,
		request.CredentialID, principal.AgentID, []byte(publicKey), fingerprint).Scan(&result.CredentialID, &result.Fingerprint, &result.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "agent_credentials_pkey" {
				return RotationResult{}, ErrCredentialIDInUse
			}
			return RotationResult{}, ErrKeyReuse
		}
		return RotationResult{}, fmt.Errorf("store rotated credential: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorAgentID: principal.AgentID, Action: "agent.credential.rotated", TargetType: "agent",
		TargetID: principal.AgentID, SourceAddress: sourceAddress, Parameters: map[string]any{
			"previousCredentialId": principal.CredentialID, "previousFingerprint": oldFingerprint,
			"credentialId": result.CredentialID, "fingerprint": result.Fingerprint}}); err != nil {
		return RotationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RotationResult{}, fmt.Errorf("commit credential rotation: %w", err)
	}
	return result, nil
}
