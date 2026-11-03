//go:build integration

package remote_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// enableReboot prepares the built-in system.reboot policy; with
// forbidSelfApproval it requires a second operator for this test.
func enableReboot(t *testing.T, forbidSelfApproval bool) {
	t.Helper()
	if forbidSelfApproval {
		remote.ForbidSelfApprovalForTest(t, agentproto.CapabilitySystemReboot)
	}
}

// systemManagementTypes are the commands the privileged helper runs (#92).
var systemManagementTypes = []string{agentproto.CapabilityServiceRestart, agentproto.CapabilityServiceStop,
	agentproto.CapabilityProcessTerminate, agentproto.CapabilitySystemReboot, agentproto.CapabilitySystemShutdown}

// privilegedTarget is an agent that reported transport, associated with a
// device in the given trust state ("" leaves the agent unassociated).
func (f fixture) privilegedTarget(t *testing.T, transport, trust string) (agents.Principal, string) {
	t.Helper()
	ctx := context.Background()
	principal := f.agent(t, systemManagementTypes...)
	facts := agentproto.HostFacts{Hostname: "host", OS: "linux", Architecture: "amd64", AgentVersion: "test"}
	if err := f.agents.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts, Capabilities: systemManagementTypes,
		Transport: transport}); err != nil {
		t.Fatal(err)
	}
	if trust == "" {
		return principal, ""
	}
	deviceID := testsupport.CreateDevice(t, f.pool, "server-"+principal.AgentID[:8], "", "")
	if _, err := f.pool.Exec(ctx, `UPDATE agents SET device_id = $2::uuid, association_status = 'confirmed', associated_at = now(), associated_by = $3::uuid
		WHERE id = $1::uuid`, principal.AgentID, deviceID, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	f.setTrust(t, deviceID, trust)
	return principal, deviceID
}

func (f fixture) setTrust(t *testing.T, deviceID, trust string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE devices SET trust_state = $2, trust_changed_by = $3::uuid, trust_changed_at = clock_timestamp()
		WHERE entity_id = $1::uuid`, deviceID, trust, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) auditCode(t *testing.T, action, targetID string) string {
	t.Helper()
	var code string
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(error_code, '') FROM audit_log WHERE action = $1 AND target_id = $2
		ORDER BY id DESC LIMIT 1`, action, targetID).Scan(&code); err != nil {
		t.Fatalf("audit %s for %s: %v", action, targetID, err)
	}
	return code
}

func TestPrivilegedCommandApprovalFlow(t *testing.T) {
	enableReboot(t, false)
	f := newFixture(t, "remote_approval")
	f.commands.SetApprovalKey(testsupport.CoreKey())
	ctx := context.Background()
	principal, deviceID := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")

	command := f.create(t, principal.AgentID, agentproto.CapabilitySystemReboot, nil)
	if command.Status != "awaiting_approval" || command.Target == nil || command.Target.DeviceID != deviceID ||
		command.Target.TrustState != "trusted" || command.Target.Transport != agentproto.TransportHTTPS || command.Target.AgentFingerprint == "" {
		t.Fatalf("unexpected awaiting command: %+v target=%+v", command, command.Target)
	}
	if _, err := f.commands.Claim(ctx, principal, 0); !errors.Is(err, remote.ErrNoCommand) {
		t.Fatalf("unapproved command was claimable: %v", err)
	}

	approved, err := f.commands.Approve(ctx, command.ID, f.actor)
	if err != nil || approved.Status != "queued" || approved.Approval == nil || approved.Approval.ApprovedBy != f.actor.UserID {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if window := approved.Approval.ExpiresAt.Sub(approved.Approval.ApprovedAt); window > agentproto.DefaultApprovalTTL || window <= 0 {
		t.Fatalf("approval window %v", window)
	}
	if _, err := f.commands.Approve(ctx, command.ID, f.actor); !errors.Is(err, remote.ErrNotAwaitingApproval) {
		t.Fatalf("second approval: %v", err)
	}

	claimed, err := f.commands.Claim(ctx, principal, 0)
	if err != nil || claimed.ID != command.ID || claimed.Approval == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	public := testsupport.CoreKey().Public().(ed25519.PublicKey)
	if err := agentproto.VerifyCommandApproval(public, claimed, time.Now()); err != nil {
		t.Fatalf("claimed approval does not verify: %v", err)
	}
	if claimed.Approval.DeviceID != deviceID {
		t.Fatalf("approval device %q", claimed.Approval.DeviceID)
	}
	for _, action := range []string{"remote.command.created", "remote.command.approved", "remote.command.claimed"} {
		if f.auditCount(t, action, command.ID) != 1 {
			t.Fatalf("missing audit %s", action)
		}
	}
}

func TestPrivilegedCommandGateRefusals(t *testing.T) {
	enableReboot(t, false)
	f := newFixture(t, "remote_gate")
	f.commands.SetApprovalKey(testsupport.CoreKey())
	ctx := context.Background()
	technicianID := testsupport.CreateUser(t, f.pool, "tech@example.invalid", "integration-password", "technician")
	technician := agents.Actor{UserID: technicianID}

	plain, _ := f.privilegedTarget(t, agentproto.TransportHTTP, "trusted")
	unreported, _ := f.privilegedTarget(t, "", "trusted")
	reviewed, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "reviewed")
	unassociated, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "")
	blocked, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "blocked")
	trusted, trustedDevice := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")

	for name, tc := range map[string]struct {
		agentID string
		actor   agents.Actor
		want    error
		code    string
	}{
		"plain HTTP":           {plain.AgentID, f.actor, remote.ErrTransportNotHTTPS, "transport_not_https"},
		"transport unreported": {unreported.AgentID, f.actor, remote.ErrTransportNotHTTPS, "transport_not_https"},
		"device only reviewed": {reviewed.AgentID, f.actor, remote.ErrDeviceNotTrusted, "device_not_trusted"},
		"no device":            {unassociated.AgentID, f.actor, remote.ErrDeviceNotTrusted, "device_not_trusted"},
		"device blocked":       {blocked.AgentID, f.actor, remote.ErrDeviceBlocked, "device_blocked"},
		"out of scope":         {trusted.AgentID, technician, remote.ErrOutOfScope, "scope_denied"},
	} {
		_, err := f.commands.Create(ctx, remote.CreateInput{AgentID: tc.agentID, Type: agentproto.CapabilitySystemReboot}, tc.actor)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
		if code := f.auditCode(t, "remote.command.refused", tc.agentID); code != tc.code {
			t.Fatalf("%s: audit code %q, want %q", name, code, tc.code)
		}
	}

	// A user scope on a device tag admits the technician for tagged devices only.
	if _, err := f.pool.Exec(ctx, `INSERT INTO remote_scopes (user_id, device_tag, command_types) VALUES ($1::uuid, 'lab', ARRAY['system.reboot'])`, technicianID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: trusted.AgentID, Type: agentproto.CapabilitySystemReboot}, technician); !errors.Is(err, remote.ErrOutOfScope) {
		t.Fatalf("untagged device: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO device_tags (device_id, tag) VALUES ($1::uuid, 'lab')`, trustedDevice); err != nil {
		t.Fatal(err)
	}
	command, err := f.commands.Create(ctx, remote.CreateInput{AgentID: trusted.AgentID, Type: agentproto.CapabilitySystemReboot}, technician)
	if err != nil || command.Status != "awaiting_approval" {
		t.Fatalf("scoped create: %+v %v", command, err)
	}

	// Approval re-checks the gate: a device demoted after the request refuses it.
	f.setTrust(t, trustedDevice, "reviewed")
	if _, err := f.commands.Approve(ctx, command.ID, f.actor); !errors.Is(err, remote.ErrDeviceNotTrusted) {
		t.Fatalf("approval after demotion: %v", err)
	}
	if code := f.auditCode(t, "remote.command.approval_refused", command.ID); code != "device_not_trusted" {
		t.Fatalf("approval refusal audit code %q", code)
	}
	f.setTrust(t, trustedDevice, "trusted")
	// The approver needs a scope of their own.
	if _, err := f.commands.Approve(ctx, command.ID, agents.Actor{UserID: testsupport.CreateUser(t, f.pool, "ro@example.invalid", "integration-password", "read_only")}); !errors.Is(err, remote.ErrOutOfScope) {
		t.Fatalf("approval without scope: %v", err)
	}
	if _, err := f.commands.Approve(ctx, command.ID, f.actor); err != nil {
		t.Fatalf("approval: %v", err)
	}
}

func TestSelfApprovalCanBeForbidden(t *testing.T) {
	enableReboot(t, true)
	f := newFixture(t, "remote_self_approval")
	f.commands.SetApprovalKey(testsupport.CoreKey())
	ctx := context.Background()
	principal, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")
	command := f.create(t, principal.AgentID, agentproto.CapabilitySystemReboot, nil)
	if _, err := f.commands.Approve(ctx, command.ID, f.actor); !errors.Is(err, remote.ErrSelfApproval) {
		t.Fatalf("self approval: %v", err)
	}
	if code := f.auditCode(t, "remote.command.approval_refused", command.ID); code != "self_approval_forbidden" {
		t.Fatalf("audit code %q", code)
	}
	second := agents.Actor{UserID: testsupport.CreateUser(t, f.pool, "second@example.invalid", "integration-password", "administrator")}
	if approved, err := f.commands.Approve(ctx, command.ID, second); err != nil || approved.Status != "queued" {
		t.Fatalf("second-operator approval: %+v %v", approved, err)
	}
}

func TestApprovalUnavailableWithoutKey(t *testing.T) {
	enableReboot(t, false)
	f := newFixture(t, "remote_approval_key")
	principal, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")
	command := f.create(t, principal.AgentID, agentproto.CapabilitySystemReboot, nil)
	if _, err := f.commands.Approve(context.Background(), command.ID, f.actor); !errors.Is(err, remote.ErrApprovalUnavailable) {
		t.Fatalf("approval without key: %v", err)
	}
}

func TestExpiredApprovalIsNeverClaimed(t *testing.T) {
	enableReboot(t, false)
	f := newFixture(t, "remote_approval_expiry")
	ctx := context.Background()
	principal, _ := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")
	command := f.create(t, principal.AgentID, agentproto.CapabilitySystemReboot, nil)

	// Approval rows are write-once, so store one that has already expired.
	if _, err := f.pool.Exec(ctx, `INSERT INTO remote_command_approvals (command_id, approved_by, approved_at, expires_at, parameters_digest, key_id, signature)
		VALUES ($1::uuid, $2::uuid, now() - interval '20 minutes', now() - interval '10 minutes', repeat('0', 64), repeat('0', 16), 'x')`,
		command.ID, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE remote_commands SET status = 'queued' WHERE id = $1::uuid`, command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Claim(ctx, principal, 0); !errors.Is(err, remote.ErrNoCommand) {
		t.Fatalf("expired approval was claimable: %v", err)
	}
	// The database refuses the transition even without the service's filter.
	if _, err := f.pool.Exec(ctx, `UPDATE remote_commands SET status = 'claimed', claimed_at = now(), claimed_by_credential = $2::uuid
		WHERE id = $1::uuid`, command.ID, principal.CredentialID); err == nil {
		t.Fatal("database allowed claiming with an expired approval")
	}
	if err := f.commands.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	expired, err := f.commands.Get(ctx, command.ID)
	if err != nil || expired.Status != "expired" || expired.FailureCode != "approval_expired" {
		t.Fatalf("after sweep: %+v %v", expired, err)
	}
	if code := f.auditCode(t, "remote.command.expired", command.ID); code != "approval_expired" {
		t.Fatalf("expiry audit code %q", code)
	}
}

func TestDatabaseGuardsApprovalStates(t *testing.T) {
	enableReboot(t, false)
	f := newFixture(t, "remote_approval_guard")
	ctx := context.Background()
	principal, deviceID := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")
	if _, err := f.pool.Exec(ctx, `INSERT INTO remote_commands (agent_id, device_id, command_type, risk, requested_by, expires_at, timeout_seconds, max_output_bytes)
		VALUES ($1::uuid, $2::uuid, 'system.reboot', 'privileged', $3::uuid, now() + interval '10 minutes', 30, 4096)`,
		principal.AgentID, deviceID, f.actor.UserID); err == nil {
		t.Fatal("a privileged command was inserted as queued")
	}
	command := f.create(t, principal.AgentID, agentproto.CapabilitySystemReboot, nil)
	if _, err := f.pool.Exec(ctx, `UPDATE remote_commands SET status = 'queued' WHERE id = $1::uuid`, command.ID); err == nil {
		t.Fatal("a command left awaiting_approval without an approval")
	}
	cancelled, err := f.commands.Cancel(ctx, command.ID, f.actor)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel awaiting command: %+v %v", cancelled, err)
	}
}

func TestSystemManagementCommands(t *testing.T) {
	f := newFixture(t, "remote_system_management")
	f.commands.SetApprovalKey(testsupport.CoreKey())
	ctx := context.Background()
	principal, deviceID := f.privilegedTarget(t, agentproto.TransportHTTPS, "trusted")
	public := testsupport.CoreKey().Public().(ed25519.PublicKey)

	// Each command is approval-gated, and its validated parameters are bound
	// into the signed approval the agent and helper verify.
	runs := []struct {
		commandType string
		parameters  map[string]any
		result      agentproto.CommandResult
		status      string
	}{
		{agentproto.CapabilityServiceRestart, map[string]any{"name": "nginx.service"},
			agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: []byte(`{"service":"nginx.service","state":"active"}`)}, "succeeded"},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 4242.0, "name": "sshd"},
			agentproto.CommandResult{Status: agentproto.CommandFailed, FailureCode: "protected_target", FailureReason: "The process sshd is protected."}, "failed"},
		{agentproto.CapabilityServiceStop, map[string]any{"name": "Spooler"},
			agentproto.CommandResult{Status: agentproto.CommandFailed, FailureCode: "unknown_service", FailureReason: "No service Spooler is installed."}, "failed"},
		{agentproto.CapabilitySystemShutdown, nil,
			agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: []byte(`{"action":"poweroff","scheduled":true}`)}, "succeeded"},
	}
	for _, run := range runs {
		command := f.create(t, principal.AgentID, run.commandType, run.parameters)
		if command.Status != "awaiting_approval" {
			t.Fatalf("%s: status %s", run.commandType, command.Status)
		}
		if _, err := f.commands.Approve(ctx, command.ID, f.actor); err != nil {
			t.Fatalf("%s: approve: %v", run.commandType, err)
		}
		claimed, err := f.commands.Claim(ctx, principal, 0)
		if err != nil || claimed.ID != command.ID {
			t.Fatalf("%s: claim: %v", run.commandType, err)
		}
		if err := agentproto.VerifyCommandApproval(public, claimed, time.Now()); err != nil || claimed.Approval.DeviceID != deviceID {
			t.Fatalf("%s: approval: %v", run.commandType, err)
		}
		// A parameter changed after approval no longer matches the signature.
		if len(claimed.Parameters) > 0 {
			tampered := claimed
			tampered.Parameters = map[string]any{"name": "other", "pid": 1.0}
			if err := agentproto.VerifyCommandApproval(public, tampered, time.Now()); !errors.Is(err, agentproto.ErrApprovalMismatch) {
				t.Fatalf("%s: tampered parameters verified: %v", run.commandType, err)
			}
		}
		if err := f.commands.Start(ctx, principal, command.ID); err != nil {
			t.Fatalf("%s: start: %v", run.commandType, err)
		}
		if _, err := f.commands.SubmitResult(ctx, principal, command.ID, run.result); err != nil {
			t.Fatalf("%s: result: %v", run.commandType, err)
		}
		stored, err := f.commands.Get(ctx, command.ID)
		if err != nil || stored.Status != run.status || stored.FailureCode != run.result.FailureCode {
			t.Fatalf("%s: stored %+v %v", run.commandType, stored, err)
		}
		for _, action := range []string{"remote.command.created", "remote.command.approved", "remote.command.claimed"} {
			if f.auditCount(t, action, command.ID) != 1 {
				t.Fatalf("%s: missing audit %s", run.commandType, action)
			}
		}
	}

	// Invalid parameters are refused before anything is stored.
	for commandType, parameters := range map[string]map[string]any{
		agentproto.CapabilityServiceRestart:   {"name": "--now"},
		agentproto.CapabilityProcessTerminate: {"pid": 1.0, "name": "systemd"},
		agentproto.CapabilitySystemReboot:     {"delaySeconds": 0.0},
	} {
		if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: principal.AgentID, Type: commandType, Parameters: parameters}, f.actor); !errors.Is(err, remote.ErrInvalidParameters) {
			t.Fatalf("%s %v: %v", commandType, parameters, err)
		}
	}

	// Scopes are per command type: a technician allowed to restart services
	// may not end processes or reboot.
	technicianID := testsupport.CreateUser(t, f.pool, "tech@example.invalid", "integration-password", "technician")
	technician := agents.Actor{UserID: technicianID}
	if _, err := f.pool.Exec(ctx, `INSERT INTO remote_scopes (user_id, device_id, command_types) VALUES ($1::uuid, $2::uuid, ARRAY['service.restart'])`,
		technicianID, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: principal.AgentID, Type: agentproto.CapabilityServiceRestart,
		Parameters: map[string]any{"name": "nginx"}}, technician); err != nil {
		t.Fatalf("scoped restart: %v", err)
	}
	for _, commandType := range []string{agentproto.CapabilityProcessTerminate, agentproto.CapabilitySystemReboot} {
		parameters := map[string]any{}
		if commandType == agentproto.CapabilityProcessTerminate {
			parameters = map[string]any{"pid": 4242.0, "name": "nginx"}
		}
		if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: principal.AgentID, Type: commandType, Parameters: parameters}, technician); !errors.Is(err, remote.ErrOutOfScope) {
			t.Fatalf("%s outside the technician's scope: %v", commandType, err)
		}
	}
}
