package remote

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/audit"
)

// Approval gate (#99). A command whose policy requires approval is created in
// 'awaiting_approval' only when every precondition holds, and an operator's
// approval re-checks them all before Core signs an approval envelope and
// queues it. Each refusal has its own error and stable code, and is audited.
var (
	ErrTransportNotHTTPS   = errors.New("the agent's last heartbeat did not report verified HTTPS; privileged commands are refused")
	ErrDeviceNotTrusted    = errors.New("privileged commands need an agent associated with a trusted device")
	ErrOutOfScope          = errors.New("no remote scope allows you this command type on this device")
	ErrNotAwaitingApproval = errors.New("the command is not awaiting approval")
	ErrCommandExpired      = errors.New("the command expired before it was approved")
	ErrSelfApproval        = errors.New("this command type needs approval by a second operator")
	ErrTargetChanged       = errors.New("the agent's device association changed after the command was requested")
	ErrApprovalUnavailable = errors.New("approval signing is not configured (NERVE_CORE_SIGNING_KEY)")
)

// GateCode returns the stable code for an approval-gate refusal, or "".
func GateCode(err error) string {
	switch {
	case errors.Is(err, ErrTransportNotHTTPS):
		return "transport_not_https"
	case errors.Is(err, ErrDeviceNotTrusted):
		return "device_not_trusted"
	case errors.Is(err, ErrDeviceBlocked):
		return "device_blocked"
	case errors.Is(err, ErrOutOfScope):
		return "scope_denied"
	case errors.Is(err, ErrNotAwaitingApproval):
		return "command_not_awaiting_approval"
	case errors.Is(err, ErrCommandExpired):
		return "command_expired"
	case errors.Is(err, ErrSelfApproval):
		return "self_approval_forbidden"
	case errors.Is(err, ErrTargetChanged):
		return "target_changed"
	case errors.Is(err, ErrAgentUnavailable):
		return "agent_unavailable"
	case errors.Is(err, ErrCapabilityNotAdvertised):
		return "capability_not_supported"
	}
	return ""
}

// Approval is the stored approval of a privileged command.
type Approval struct {
	ApprovedBy       string    `json:"approvedBy"`
	ApprovedByEmail  string    `json:"approvedByEmail,omitempty"`
	ApprovedAt       time.Time `json:"approvedAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	ParametersDigest string    `json:"parametersDigest"`
}

// Target is what an operator must see before approving: who the command will
// reach and why NERVE believes that agent belongs to that device.
type Target struct {
	AgentID             string            `json:"agentId"`
	AgentName           string            `json:"agentName"`
	Hostname            string            `json:"hostname,omitempty"`
	AgentFingerprint    string            `json:"agentFingerprint,omitempty"`
	Transport           string            `json:"transport,omitempty"`
	DeviceID            string            `json:"deviceId,omitempty"`
	DeviceName          string            `json:"deviceName,omitempty"`
	TrustState          string            `json:"trustState,omitempty"`
	AssociationStatus   string            `json:"associationStatus"`
	AssociationEvidence []agents.Evidence `json:"associationEvidence"`
}

// SetApprovalKey sets the Core key that signs approvals. Without it nothing
// can be approved.
func (s *Service) SetApprovalKey(key ed25519.PrivateKey) {
	s.approvalKey = key
}

// gateTarget is the locked state the gate decides on.
type gateTarget struct {
	agentID, deviceID, transport, trust string
}

// checkGate enforces the preconditions for a privileged command, in a fixed
// order so each refusal is deterministic: transport, device trust, scope. The
// caller holds the agent row and the device row (FOR SHARE).
func checkGate(ctx context.Context, tx pgx.Tx, target gateTarget, commandType string, actor agents.Actor) error {
	if target.transport != agentproto.TransportHTTPS {
		return ErrTransportNotHTTPS
	}
	if target.deviceID == "" || target.trust != "trusted" {
		return ErrDeviceNotTrusted
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM remote_scopes scope
		WHERE (scope.user_id = $1::uuid OR scope.role_id IN (SELECT role_id FROM user_roles WHERE user_id = $1::uuid))
		  AND (scope.command_types @> ARRAY[$2::text] OR scope.command_types @> ARRAY['*'])
		  AND ((scope.device_id IS NULL AND scope.device_tag IS NULL) OR scope.device_id = $3::uuid
			OR scope.device_tag IN (SELECT tag FROM device_tags WHERE device_id = $3::uuid)))`,
		actor.UserID, commandType, target.deviceID).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("check remote scope: %w", err)
	}
	if !allowed {
		return ErrOutOfScope
	}
	return nil
}

// auditRefusal records a gate refusal outside the rolled-back transaction.
func (s *Service) auditRefusal(ctx context.Context, action, targetType, targetID string, actor agents.Actor, err error, parameters map[string]any) {
	code := GateCode(err)
	if code == "" {
		return
	}
	_ = audit.Write(ctx, s.db, audit.Entry{ActorUserID: actor.UserID, Action: action, TargetType: targetType, TargetID: targetID,
		SourceAddress: actor.SourceAddress, Result: audit.ResultDenied, ErrorCode: code, Parameters: parameters})
}

// Approve checks the gate again for the approving operator, signs an approval
// envelope and queues the command. The approval is valid for
// agentproto.DefaultApprovalTTL or until the command expires, whichever is
// sooner.
func (s *Service) Approve(ctx context.Context, commandID string, actor agents.Actor) (Command, error) {
	command, err := s.approve(ctx, commandID, actor)
	if err != nil {
		s.auditRefusal(ctx, "remote.command.approval_refused", "remote_command", commandID, actor, err, nil)
		return Command{}, err
	}
	s.notifier.notify(command.AgentID)
	return command, nil
}

func (s *Service) approve(ctx context.Context, commandID string, actor agents.Actor) (Command, error) {
	if s.approvalKey == nil {
		return Command{}, ErrApprovalUnavailable
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Command{}, fmt.Errorf("begin approval: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var (
		status, agentID, deviceID, commandType, requestedBy string
		rawParameters                                       []byte
		expiresAt, now                                      time.Time
	)
	// Lock the agent before the command, the order revocation uses
	// (CancelForAgent), so the two cannot deadlock. A command's agent never
	// changes, so reading it unlocked first is safe.
	err = tx.QueryRow(ctx, `SELECT agent_id::text FROM remote_commands WHERE id = $1::uuid`, commandID).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	if err != nil {
		return Command{}, fmt.Errorf("read command agent: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM agents WHERE id = $1::uuid FOR NO KEY UPDATE`, agentID); err != nil {
		return Command{}, fmt.Errorf("lock command agent: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT status, COALESCE(device_id::text, ''), command_type, parameters, requested_by::text,
		expires_at, now() FROM remote_commands WHERE id = $1::uuid FOR UPDATE`, commandID).Scan(
		&status, &deviceID, &commandType, &rawParameters, &requestedBy, &expiresAt, &now)
	if err != nil {
		return Command{}, fmt.Errorf("lock command approval: %w", err)
	}
	if status != "awaiting_approval" {
		return Command{}, ErrNotAwaitingApproval
	}
	policy, ok := PolicyFor(commandType)
	if !ok || !policy.RequiresApproval {
		return Command{}, ErrUnknownCommand
	}
	if policy.ForbidSelfApproval && requestedBy == actor.UserID {
		return Command{}, ErrSelfApproval
	}
	approvalExpiry := now.Add(agentproto.DefaultApprovalTTL)
	if expiresAt.Before(approvalExpiry) {
		approvalExpiry = expiresAt
	}
	// Leave the agent at least a few seconds to claim it.
	if approvalExpiry.Sub(now) < 5*time.Second {
		return Command{}, ErrCommandExpired
	}
	target, err := lockGateTarget(ctx, tx, agentID, commandType)
	if err != nil {
		return Command{}, err
	}
	if target.deviceID != deviceID {
		return Command{}, ErrTargetChanged
	}
	if err := checkGate(ctx, tx, target, commandType, actor); err != nil {
		return Command{}, err
	}
	parameters := map[string]any{}
	if err := json.Unmarshal(rawParameters, &parameters); err != nil {
		return Command{}, fmt.Errorf("decode command parameters: %w", err)
	}
	envelope := agentproto.SignCommandApproval(s.approvalKey, agentproto.CommandApproval{CommandID: commandID, AgentID: agentID,
		DeviceID: deviceID, Type: commandType, ParametersDigest: agentproto.ParametersDigest(parameters), ApprovedBy: actor.UserID,
		ApprovedAt: now, ExpiresAt: approvalExpiry})
	if _, err := tx.Exec(ctx, `INSERT INTO remote_command_approvals (command_id, approved_by, approved_at, expires_at, parameters_digest, key_id, signature)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`, commandID, actor.UserID, envelope.ApprovedAt, envelope.ExpiresAt,
		envelope.ParametersDigest, envelope.KeyID, envelope.Signature); err != nil {
		return Command{}, fmt.Errorf("store approval: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE remote_commands SET status = 'queued' WHERE id = $1::uuid`, commandID); err != nil {
		return Command{}, fmt.Errorf("queue approved command: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "remote.command.approved", TargetType: "remote_command",
		TargetID: commandID, SourceAddress: actor.SourceAddress, Parameters: map[string]any{"agentId": agentID, "deviceId": deviceID,
			"type": commandType, "parametersDigest": envelope.ParametersDigest, "approvalExpiresAt": envelope.ExpiresAt,
			"selfApproved": requestedBy == actor.UserID, "keyId": envelope.KeyID}}); err != nil {
		return Command{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Command{}, fmt.Errorf("commit approval: %w", err)
	}
	return s.Get(ctx, commandID)
}

// lockGateTarget locks the agent (FOR NO KEY UPDATE, like Create) and its
// device (FOR SHARE, serializing with trust changes) and checks the agent can
// still take this command.
func lockGateTarget(ctx context.Context, tx pgx.Tx, agentID, commandType string) (gateTarget, error) {
	target := gateTarget{agentID: agentID}
	var (
		status       string
		capabilities []string
	)
	err := tx.QueryRow(ctx, `SELECT status, capabilities, COALESCE(device_id::text, ''), COALESCE(transport, '')
		FROM agents WHERE id = $1::uuid FOR NO KEY UPDATE`, agentID).Scan(&status, &capabilities, &target.deviceID, &target.transport)
	if errors.Is(err, pgx.ErrNoRows) {
		return gateTarget{}, agents.ErrNotFound
	}
	if err != nil {
		return gateTarget{}, fmt.Errorf("lock command agent: %w", err)
	}
	if status != "active" {
		return gateTarget{}, ErrAgentUnavailable
	}
	if !contains(capabilities, commandType) {
		return gateTarget{}, ErrCapabilityNotAdvertised
	}
	if target.deviceID != "" {
		if err := tx.QueryRow(ctx, `SELECT trust_state FROM devices WHERE entity_id = $1::uuid FOR SHARE`, target.deviceID).Scan(&target.trust); err != nil {
			return gateTarget{}, fmt.Errorf("read device trust: %w", err)
		}
		// A blocked device gets no remote actions at all.
		if target.trust == "blocked" {
			return gateTarget{}, ErrDeviceBlocked
		}
	}
	return target, nil
}

// TargetFor summarizes the command's target for the approval dialog.
func (s *Service) TargetFor(ctx context.Context, agentID string) (Target, error) {
	var (
		target   Target
		evidence []byte
	)
	err := s.db.QueryRow(ctx, `SELECT a.id::text, a.display_name, COALESCE(a.hostname, ''), COALESCE(credential.fingerprint, ''),
		COALESCE(a.transport, ''), COALESCE(a.device_id::text, ''), COALESCE(device_entity.name, ''), COALESCE(d.trust_state, ''),
		a.association_status, a.association_evidence
		FROM agents a
		LEFT JOIN agent_credentials credential ON credential.agent_id = a.id AND credential.revoked_at IS NULL
		LEFT JOIN entities device_entity ON device_entity.id = a.device_id
		LEFT JOIN devices d ON d.entity_id = a.device_id
		WHERE a.id = $1::uuid`, agentID).Scan(&target.AgentID, &target.AgentName, &target.Hostname, &target.AgentFingerprint,
		&target.Transport, &target.DeviceID, &target.DeviceName, &target.TrustState, &target.AssociationStatus, &evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, agents.ErrNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("read command target: %w", err)
	}
	target.AssociationEvidence = []agents.Evidence{}
	if len(evidence) > 0 {
		_ = json.Unmarshal(evidence, &target.AssociationEvidence)
	}
	return target, nil
}
