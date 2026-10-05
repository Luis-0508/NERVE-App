package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/audit"
)

const (
	DefaultCommandExpiry = 10 * time.Minute
	MaxActivePerAgent    = 20
	// ResultGrace is how long after its timeout a claimed command may still
	// report before the sweeper marks it timed_out.
	ResultGrace     = time.Minute
	ResultRetention = 30 * 24 * time.Hour
)

var (
	ErrUnknownCommand          = errors.New("this command type is not enabled")
	ErrInvalidParameters       = errors.New("invalid command parameters")
	ErrCapabilityNotAdvertised = errors.New("the agent does not advertise this capability")
	ErrAgentUnavailable        = errors.New("the agent is revoked or removed")
	ErrQueueFull               = errors.New("the agent already has too many pending commands")
	ErrNotFound                = errors.New("remote command not found")
	ErrNotActive               = errors.New("remote command is no longer active")
	ErrCancelled               = errors.New("remote command was cancelled")
	ErrAlreadyCompleted        = errors.New("remote command already has a different result")
	ErrResultTooLarge          = errors.New("remote command result exceeds the output limit")
	ErrInvalidResult           = errors.New("invalid remote command result")
	ErrNoCommand               = errors.New("no command is queued")
	ErrDeviceBlocked           = errors.New("the agent's device is blocked; remote actions are refused")

	failureCodePattern = regexp.MustCompile(`^[a-z_]{1,64}$`)

	// reservedFailureCodes are written only by Core, so their presence proves
	// that no agent result exists for the command.
	reservedFailureCodes = map[string]bool{"no_result": true, "expired": true, "cancelled": true, "agent_revoked": true, "agent_removed": true}
)

type Result struct {
	Output          json.RawMessage `json:"output,omitempty"`
	Stdout          string          `json:"stdout,omitempty"`
	Stderr          string          `json:"stderr,omitempty"`
	OutputTruncated bool            `json:"outputTruncated"`
	StdoutTruncated bool            `json:"stdoutTruncated"`
	StderrTruncated bool            `json:"stderrTruncated"`
	OutputBytes     int             `json:"outputBytes"`
	Digest          string          `json:"digest"`
	SubmittedAt     time.Time       `json:"submittedAt"`
}

type Command struct {
	ID               string         `json:"id"`
	Provider         string         `json:"provider"`
	AgentID          string         `json:"agentId"`
	AgentName        string         `json:"agentName"`
	DeviceID         string         `json:"deviceId,omitempty"`
	Type             string         `json:"type"`
	Risk             string         `json:"risk"`
	Parameters       map[string]any `json:"parameters"`
	RequestedBy      string         `json:"requestedBy"`
	RequestedByEmail string         `json:"requestedByEmail,omitempty"`
	RequestedAt      time.Time      `json:"requestedAt"`
	ExpiresAt        time.Time      `json:"expiresAt"`
	TimeoutSeconds   int            `json:"timeoutSeconds"`
	MaxOutputBytes   int            `json:"maxOutputBytes"`
	Status           string         `json:"status"`
	ClaimedAt        *time.Time     `json:"claimedAt,omitempty"`
	StartedAt        *time.Time     `json:"startedAt,omitempty"`
	CompletedAt      *time.Time     `json:"completedAt,omitempty"`
	ExitCode         *int           `json:"exitCode,omitempty"`
	FailureCode      string         `json:"failureCode,omitempty"`
	FailureReason    string         `json:"failureReason,omitempty"`
	CancelledBy      string         `json:"cancelledBy,omitempty"`
	Result           *Result        `json:"result,omitempty"`
	ResultPruned     bool           `json:"resultPruned,omitempty"`
}

type CreateInput struct {
	AgentID          string
	Type             string
	Parameters       map[string]any
	TimeoutSeconds   int
	ExpiresInSeconds int
}

// Service is the command pipeline for the native agent provider.
type Service struct {
	db       *pgxpool.Pool
	notifier *notifier
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db, notifier: newNotifier()}
}

// Create validates and queues a command. The caller has already checked the
// policy's permission for the authenticated operator.
func (s *Service) Create(ctx context.Context, input CreateInput, actor agents.Actor) (Command, error) {
	policy, ok := PolicyFor(input.Type)
	if !ok {
		return Command{}, ErrUnknownCommand
	}
	parameters, err := policy.Normalize(input.Parameters)
	if err != nil {
		return Command{}, err
	}
	timeout := policy.DefaultTimeout
	if input.TimeoutSeconds != 0 {
		timeout = time.Duration(input.TimeoutSeconds) * time.Second
		if timeout < time.Second || timeout > policy.MaxTimeout {
			return Command{}, fmt.Errorf("%w: timeout must be between 1 and %d seconds", ErrInvalidParameters, int(policy.MaxTimeout.Seconds()))
		}
	}
	expiry := DefaultCommandExpiry
	if input.ExpiresInSeconds != 0 {
		expiry = time.Duration(input.ExpiresInSeconds) * time.Second
		if expiry < 30*time.Second || expiry > time.Hour {
			return Command{}, fmt.Errorf("%w: expiry must be between 30 and 3600 seconds", ErrInvalidParameters)
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Command{}, fmt.Errorf("begin command: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var (
		status, deviceID string
		capabilities     []string
	)
	// FOR NO KEY UPDATE serializes with revocation (FOR UPDATE) and with other
	// creates for the same agent, so the active-command limit below is exact.
	err = tx.QueryRow(ctx, `SELECT status, capabilities, COALESCE(device_id::text, '') FROM agents WHERE id = $1::uuid FOR NO KEY UPDATE`, input.AgentID).Scan(&status, &capabilities, &deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, agents.ErrNotFound
	}
	if err != nil {
		return Command{}, fmt.Errorf("lock command agent: %w", err)
	}
	if status != "active" {
		return Command{}, ErrAgentUnavailable
	}
	if !contains(capabilities, input.Type) {
		return Command{}, ErrCapabilityNotAdvertised
	}
	// Device trust is operator-owned. A blocked device gets no remote actions;
	// future privileged policies will additionally require "trusted".
	if deviceID != "" {
		var trust string
		// FOR SHARE serializes with SetTrust (FOR UPDATE): a device blocked
		// concurrently cannot slip a command through with its old trust state.
		if err := tx.QueryRow(ctx, `SELECT trust_state FROM devices WHERE entity_id = $1::uuid FOR SHARE`, deviceID).Scan(&trust); err != nil {
			return Command{}, fmt.Errorf("read device trust: %w", err)
		}
		if trust == "blocked" {
			return Command{}, ErrDeviceBlocked
		}
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM remote_commands WHERE agent_id = $1::uuid AND status IN ('queued', 'claimed', 'running')`, input.AgentID).Scan(&active); err != nil {
		return Command{}, fmt.Errorf("count active commands: %w", err)
	}
	if active >= MaxActivePerAgent {
		return Command{}, ErrQueueFull
	}
	encoded, _ := json.Marshal(parameters)
	var commandID string
	err = tx.QueryRow(ctx, `INSERT INTO remote_commands (agent_id, device_id, command_type, risk, parameters, requested_by, expires_at, timeout_seconds, max_output_bytes)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5::jsonb, $6::uuid, now() + $7::interval, $8, $9) RETURNING id::text`,
		input.AgentID, deviceID, policy.Type, string(policy.Risk), string(encoded), actor.UserID,
		fmt.Sprintf("%d seconds", int64(expiry.Seconds())), int(timeout.Seconds()), policy.MaxOutputBytes).Scan(&commandID)
	if err != nil {
		return Command{}, fmt.Errorf("queue command: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "remote.command.created", TargetType: "remote_command", TargetID: commandID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"agentId": input.AgentID, "deviceId": deviceID, "type": policy.Type,
			"risk": string(policy.Risk), "parameters": parameters, "timeoutSeconds": int(timeout.Seconds()), "expiresInSeconds": int(expiry.Seconds())}}); err != nil {
		return Command{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Command{}, fmt.Errorf("commit command: %w", err)
	}
	s.notifier.notify(input.AgentID)
	return s.Get(ctx, commandID)
}

// Claim hands the oldest queued command to the authenticated agent, waiting up
// to wait for one to arrive. Claiming is a single atomic UPDATE, so a command
// can never be delivered twice.
func (s *Service) Claim(ctx context.Context, principal agents.Principal, wait time.Duration) (agentproto.Command, error) {
	if wait < 0 {
		wait = 0
	}
	if wait > agentproto.MaxClaimWait {
		wait = agentproto.MaxClaimWait
	}
	deadline := time.Now().Add(wait)
	for {
		signal, release := s.notifier.subscribe(principal.AgentID)
		command, err := s.claimOnce(ctx, principal)
		if !errors.Is(err, ErrNoCommand) {
			release()
			return command, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			release()
			return agentproto.Command{}, ErrNoCommand
		}
		// Re-check periodically so commands queued through another API
		// instance are still delivered without a notification.
		if remaining > 3*time.Second {
			remaining = 3 * time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return agentproto.Command{}, ctx.Err()
		case <-signal:
		case <-timer.C:
		}
		timer.Stop()
		release()
	}
}

func (s *Service) claimOnce(ctx context.Context, principal agents.Principal) (agentproto.Command, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return agentproto.Command{}, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var (
		command    agentproto.Command
		parameters []byte
		claimedAt  time.Time
	)
	err = tx.QueryRow(ctx, `WITH next AS (
			SELECT id FROM remote_commands
			WHERE agent_id = $1::uuid AND status = 'queued' AND expires_at > now()
			ORDER BY requested_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE remote_commands command SET status = 'claimed', claimed_at = now(), claimed_by_credential = $2::uuid
		FROM next WHERE command.id = next.id
		RETURNING command.id::text, command.agent_id::text, command.command_type, command.parameters,
			command.timeout_seconds, command.max_output_bytes, command.claimed_at`, principal.AgentID, principal.CredentialID).Scan(
		&command.ID, &command.AgentID, &command.Type, &parameters, &command.TimeoutSeconds, &command.MaxOutputBytes, &claimedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentproto.Command{}, ErrNoCommand
	}
	if err != nil {
		return agentproto.Command{}, fmt.Errorf("claim command: %w", err)
	}
	if err := json.Unmarshal(parameters, &command.Parameters); err != nil {
		return agentproto.Command{}, fmt.Errorf("decode command parameters: %w", err)
	}
	command.Deadline = claimedAt.Add(time.Duration(command.TimeoutSeconds) * time.Second).UTC()
	if err := audit.Write(ctx, tx, audit.Entry{ActorAgentID: principal.AgentID, Action: "remote.command.claimed", TargetType: "remote_command",
		TargetID: command.ID, Parameters: map[string]any{"type": command.Type, "credentialId": principal.CredentialID}}); err != nil {
		return agentproto.Command{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return agentproto.Command{}, fmt.Errorf("commit claim: %w", err)
	}
	return command, nil
}

// Start marks a claimed command as running. Agents call it immediately before
// executing so a cancellation between claim and execution is honoured.
func (s *Service) Start(ctx context.Context, principal agents.Principal, commandID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin command start: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status, owner string
	err = tx.QueryRow(ctx, `SELECT status, agent_id::text FROM remote_commands WHERE id = $1::uuid FOR UPDATE`, commandID).Scan(&status, &owner)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != principal.AgentID) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock command start: %w", err)
	}
	switch status {
	case "running":
		return nil // A retried start is harmless.
	case "cancelled":
		return ErrCancelled
	case "claimed":
	default:
		return ErrNotActive
	}
	if _, err := tx.Exec(ctx, `UPDATE remote_commands SET status = 'running', started_at = now() WHERE id = $1::uuid`, commandID); err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorAgentID: principal.AgentID, Action: "remote.command.started", TargetType: "remote_command", TargetID: commandID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func resultDigest(result agentproto.CommandResult) string {
	canonical := result
	if len(canonical.Output) > 0 {
		var value any
		if json.Unmarshal(canonical.Output, &value) == nil {
			canonical.Output, _ = json.Marshal(value)
		}
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validateResult(result agentproto.CommandResult) error {
	switch result.Status {
	case agentproto.CommandSucceeded, agentproto.CommandFailed, agentproto.CommandTimedOut:
	default:
		return fmt.Errorf("%w: status must be succeeded, failed or timed_out", ErrInvalidResult)
	}
	if result.FailureCode != "" && !failureCodePattern.MatchString(result.FailureCode) {
		return fmt.Errorf("%w: failure code must be lower_snake_case", ErrInvalidResult)
	}
	if len(result.FailureReason) > 1000 {
		return fmt.Errorf("%w: failure reason is too long", ErrInvalidResult)
	}
	if result.Status != agentproto.CommandSucceeded && result.FailureCode == "" {
		return fmt.Errorf("%w: a failed result needs a failure code", ErrInvalidResult)
	}
	if reservedFailureCodes[result.FailureCode] {
		return fmt.Errorf("%w: failure code %q is reserved for Core", ErrInvalidResult, result.FailureCode)
	}
	// PostgreSQL rejects NUL in text and U+0000 in jsonb; refuse them up front
	// so the agent gets a 422 instead of an internal error.
	if strings.ContainsRune(result.Stdout, 0) || strings.ContainsRune(result.Stderr, 0) || strings.ContainsRune(result.FailureReason, 0) ||
		strings.ContainsRune(result.FailureCode, 0) || jsonContainsNUL(result.Output) {
		return fmt.Errorf("%w: results must not contain NUL characters", ErrInvalidResult)
	}
	if len(result.Output) > 0 {
		trimmed := strings.TrimSpace(string(result.Output))
		if !json.Valid(result.Output) || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
			return fmt.Errorf("%w: output must be a JSON object or array", ErrInvalidResult)
		}
	}
	return nil
}

// SubmitResult stores the single result of a command claimed by principal.
// An identical resubmission is acknowledged as a duplicate; anything else for
// a finished command is rejected.
func (s *Service) SubmitResult(ctx context.Context, principal agents.Principal, commandID string, result agentproto.CommandResult) (agentproto.ResultAck, error) {
	if err := validateResult(result); err != nil {
		return agentproto.ResultAck{}, err
	}
	digest := resultDigest(result)
	outputBytes := len(result.Output) + len(result.Stdout) + len(result.Stderr)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("begin result: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var (
		status, owner, commandType, deviceID string
		maxOutput                            int
		claimedAt                            *time.Time
		storedDigest                         string
	)
	err = tx.QueryRow(ctx, `SELECT c.status, c.agent_id::text, c.command_type, COALESCE(c.device_id::text, ''), c.max_output_bytes, c.claimed_at,
		COALESCE(r.result_digest, '') FROM remote_commands c LEFT JOIN remote_command_results r ON r.command_id = c.id
		WHERE c.id = $1::uuid FOR UPDATE OF c`, commandID).Scan(&status, &owner, &commandType, &deviceID, &maxOutput, &claimedAt, &storedDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentproto.ResultAck{}, ErrNotFound
	}
	if err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("lock command result: %w", err)
	}
	if owner != principal.AgentID {
		_ = tx.Rollback(ctx)
		_ = audit.Write(ctx, s.db, audit.Entry{ActorAgentID: principal.AgentID, Action: "remote.command.result_rejected", TargetType: "remote_command",
			TargetID: commandID, Result: audit.ResultDenied, ErrorCode: "wrong_agent"})
		return agentproto.ResultAck{}, ErrNotFound
	}
	switch status {
	case "claimed", "running":
	case "cancelled":
		return agentproto.ResultAck{}, ErrCancelled
	case "queued", "expired":
		return agentproto.ResultAck{}, ErrNotActive
	default:
		if storedDigest != "" && storedDigest == digest {
			return agentproto.ResultAck{Status: status, Duplicate: true}, nil
		}
		return agentproto.ResultAck{}, ErrAlreadyCompleted
	}
	if outputBytes > maxOutput {
		return agentproto.ResultAck{}, ErrResultTooLarge
	}
	failureCode := result.FailureCode
	if result.Status == agentproto.CommandSucceeded {
		failureCode = ""
	}
	if _, err := tx.Exec(ctx, `UPDATE remote_commands SET status = $2, completed_at = now(), exit_code = $3,
		failure_code = NULLIF($4, ''), failure_reason = NULLIF($5, '') WHERE id = $1::uuid`,
		commandID, result.Status, result.ExitCode, failureCode, strings.TrimSpace(result.FailureReason)); err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("complete command: %w", err)
	}
	var output any
	if len(result.Output) > 0 {
		output = string(result.Output)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO remote_command_results (command_id, output, stdout, stderr, output_truncated, stdout_truncated,
		stderr_truncated, output_bytes, result_digest) VALUES ($1::uuid, $2::jsonb, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8, $9)`,
		commandID, output, result.Stdout, result.Stderr, result.OutputTruncated, result.StdoutTruncated, result.StderrTruncated, outputBytes, digest); err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("store command result: %w", err)
	}
	auditResult := audit.ResultSuccess
	if result.Status != agentproto.CommandSucceeded {
		auditResult = audit.ResultFailure
	}
	durationMS := int64(0)
	if claimedAt != nil {
		durationMS = time.Since(*claimedAt).Milliseconds()
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorAgentID: principal.AgentID, Action: "remote.command.completed", TargetType: "remote_command",
		TargetID: commandID, Result: auditResult, ErrorCode: failureCode, Parameters: map[string]any{"type": commandType, "status": result.Status,
			"exitCode": result.ExitCode, "outputBytes": outputBytes, "outputTruncated": result.OutputTruncated || result.StdoutTruncated || result.StderrTruncated,
			"resultDigest": digest, "durationMs": durationMS}}); err != nil {
		return agentproto.ResultAck{}, err
	}
	severity, verb := "info", "succeeded"
	if result.Status != agentproto.CommandSucceeded {
		severity, verb = "warning", strings.ReplaceAll(result.Status, "_", " ")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details)
		VALUES (NULLIF($1, '')::uuid, 'remote.command.completed', $2, 'remote-management', $3, jsonb_build_object('commandId', $4::text, 'status', $5::text))`,
		deviceID, severity, fmt.Sprintf("Remote command %s %s.", commandType, verb), commandID, result.Status); err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("record command event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return agentproto.ResultAck{}, fmt.Errorf("commit command result: %w", err)
	}
	return agentproto.ResultAck{Status: result.Status}, nil
}

// Cancel stops a command that has not finished. A running agent learns about
// it when its result is refused; read-only commands have no side effects.
func (s *Service) Cancel(ctx context.Context, commandID string, actor agents.Actor) (Command, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Command{}, fmt.Errorf("begin cancel: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var previous string
	err = tx.QueryRow(ctx, `SELECT status FROM remote_commands WHERE id = $1::uuid FOR UPDATE`, commandID).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	if err != nil {
		return Command{}, fmt.Errorf("lock command cancel: %w", err)
	}
	if previous != "queued" && previous != "claimed" && previous != "running" {
		return Command{}, ErrNotActive
	}
	if _, err := tx.Exec(ctx, `UPDATE remote_commands SET status = 'cancelled', completed_at = now(), cancelled_by = $2::uuid,
		failure_code = 'cancelled', failure_reason = 'Cancelled by an operator.' WHERE id = $1::uuid`, commandID, actor.UserID); err != nil {
		return Command{}, fmt.Errorf("cancel command: %w", err)
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "remote.command.cancelled", TargetType: "remote_command",
		TargetID: commandID, SourceAddress: actor.SourceAddress, Parameters: map[string]any{"previousStatus": previous}}); err != nil {
		return Command{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Command{}, fmt.Errorf("commit cancel: %w", err)
	}
	return s.Get(ctx, commandID)
}

// CancelForAgent is an agents.LifecycleHook: revoking or removing an agent
// cancels everything it has not finished, in the same transaction.
func (s *Service) CancelForAgent(ctx context.Context, tx pgx.Tx, agentID string, actor agents.Actor, reason string) error {
	rows, err := tx.Query(ctx, `UPDATE remote_commands SET status = 'cancelled', completed_at = now(), cancelled_by = $2::uuid,
		failure_code = $3, failure_reason = 'The agent credential was revoked or the agent was removed.'
		WHERE agent_id = $1::uuid AND status IN ('queued', 'claimed', 'running') RETURNING id::text`, agentID, actor.UserID, "agent_"+reason)
	if err != nil {
		return fmt.Errorf("cancel agent commands: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "remote.command.cancelled", TargetType: "remote_command",
			TargetID: id, SourceAddress: actor.SourceAddress, Parameters: map[string]any{"reason": "agent_" + reason}}); err != nil {
			return err
		}
	}
	return nil
}

// Sweep expires unclaimed commands, times out claimed ones that never
// reported, and prunes old results. It is safe to run concurrently.
func (s *Service) Sweep(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin command sweep: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	type change struct{ id, status string }
	var changes []change
	collect := func(query string, args ...any) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item change
			if err := rows.Scan(&item.id, &item.status); err != nil {
				return err
			}
			changes = append(changes, item)
		}
		return rows.Err()
	}
	if err := collect(`UPDATE remote_commands SET status = 'expired', completed_at = now(), failure_code = 'expired',
		failure_reason = 'The agent did not claim the command before it expired.'
		WHERE id IN (SELECT id FROM remote_commands WHERE status = 'queued' AND expires_at <= now() FOR UPDATE SKIP LOCKED)
		RETURNING id::text, status`); err != nil {
		return fmt.Errorf("expire commands: %w", err)
	}
	if err := collect(`UPDATE remote_commands SET status = 'timed_out', completed_at = now(), failure_code = 'no_result',
		failure_reason = 'The agent did not report a result before the timeout.'
		WHERE id IN (SELECT id FROM remote_commands WHERE status IN ('claimed', 'running')
			AND claimed_at + make_interval(secs => timeout_seconds + $1) < now() FOR UPDATE SKIP LOCKED)
		RETURNING id::text, status`, int(ResultGrace.Seconds())); err != nil {
		return fmt.Errorf("time out commands: %w", err)
	}
	for _, item := range changes {
		action := "remote.command.expired"
		if item.status == "timed_out" {
			action = "remote.command.timed_out"
		}
		if err := audit.Write(ctx, tx, audit.Entry{Action: action, TargetType: "remote_command", TargetID: item.id, Result: audit.ResultFailure, ErrorCode: item.status}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM remote_command_results WHERE submitted_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int64(ResultRetention.Seconds()))); err != nil {
		return fmt.Errorf("prune command results: %w", err)
	}
	return tx.Commit(ctx)
}

const commandColumns = `c.id::text, c.provider, c.agent_id::text, a.display_name, COALESCE(c.device_id::text, ''), c.command_type, c.risk,
	c.parameters, c.requested_by::text, COALESCE(u.email, ''), c.requested_at, c.expires_at, c.timeout_seconds, c.max_output_bytes,
	c.status, c.claimed_at, c.started_at, c.completed_at, c.exit_code, COALESCE(c.failure_code, ''), COALESCE(c.failure_reason, ''),
	COALESCE(c.cancelled_by::text, '')`

func scanCommand(row pgx.Row, extra ...any) (Command, error) {
	var command Command
	var parameters []byte
	destinations := append([]any{&command.ID, &command.Provider, &command.AgentID, &command.AgentName, &command.DeviceID, &command.Type, &command.Risk,
		&parameters, &command.RequestedBy, &command.RequestedByEmail, &command.RequestedAt, &command.ExpiresAt, &command.TimeoutSeconds,
		&command.MaxOutputBytes, &command.Status, &command.ClaimedAt, &command.StartedAt, &command.CompletedAt, &command.ExitCode,
		&command.FailureCode, &command.FailureReason, &command.CancelledBy}, extra...)
	if err := row.Scan(destinations...); err != nil {
		return Command{}, err
	}
	command.Parameters = map[string]any{}
	_ = json.Unmarshal(parameters, &command.Parameters)
	return command, nil
}

// Get returns a command with its result, if one is still retained.
func (s *Service) Get(ctx context.Context, commandID string) (Command, error) {
	var (
		output                                    []byte
		stdout, stderr, digest                    *string
		outputTruncated, stdoutTrunc, stderrTrunc *bool
		outputBytes                               *int
		submittedAt                               *time.Time
	)
	command, err := scanCommand(s.db.QueryRow(ctx, `SELECT `+commandColumns+`, r.output, r.stdout, r.stderr, r.output_truncated,
		r.stdout_truncated, r.stderr_truncated, r.output_bytes, r.result_digest, r.submitted_at
		FROM remote_commands c JOIN agents a ON a.id = c.agent_id LEFT JOIN users u ON u.id = c.requested_by
		LEFT JOIN remote_command_results r ON r.command_id = c.id WHERE c.id = $1::uuid`, commandID),
		&output, &stdout, &stderr, &outputTruncated, &stdoutTrunc, &stderrTrunc, &outputBytes, &digest, &submittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	if err != nil {
		return Command{}, fmt.Errorf("read command: %w", err)
	}
	if digest != nil {
		command.Result = &Result{Output: output, Stdout: deref(stdout), Stderr: deref(stderr), OutputTruncated: *outputTruncated,
			StdoutTruncated: *stdoutTrunc, StderrTruncated: *stderrTrunc, OutputBytes: *outputBytes, Digest: *digest, SubmittedAt: *submittedAt}
	} else if (command.Status == "succeeded" || command.Status == "failed" || command.Status == "timed_out") && !reservedFailureCodes[command.FailureCode] {
		command.ResultPruned = true
	}
	return command, nil
}

// List returns recent commands for an agent or a device, without output.
func (s *Service) List(ctx context.Context, agentID, deviceID string, limit int) ([]Command, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+commandColumns+` FROM remote_commands c JOIN agents a ON a.id = c.agent_id
		LEFT JOIN users u ON u.id = c.requested_by
		WHERE (NULLIF($1, '') IS NULL OR c.agent_id = NULLIF($1, '')::uuid)
		  AND (NULLIF($2, '') IS NULL OR c.device_id = NULLIF($2, '')::uuid
			OR c.device_id IN (SELECT entity_id FROM devices WHERE merged_into = NULLIF($2, '')::uuid))
		ORDER BY c.requested_at DESC, c.id LIMIT $3`, agentID, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list commands: %w", err)
	}
	defer rows.Close()
	items := make([]Command, 0)
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		items = append(items, command)
	}
	return items, rows.Err()
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// notifier wakes long-polling claims in this process when a command is queued.
type notifier struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newNotifier() *notifier {
	return &notifier{waiters: map[string]map[chan struct{}]struct{}{}}
}

func (n *notifier) subscribe(agentID string) (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	n.mu.Lock()
	if n.waiters[agentID] == nil {
		n.waiters[agentID] = map[chan struct{}]struct{}{}
	}
	n.waiters[agentID][channel] = struct{}{}
	n.mu.Unlock()
	return channel, func() {
		n.mu.Lock()
		delete(n.waiters[agentID], channel)
		if len(n.waiters[agentID]) == 0 {
			delete(n.waiters, agentID)
		}
		n.mu.Unlock()
	}
}

func (n *notifier) notify(agentID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for channel := range n.waiters[agentID] {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

// jsonContainsNUL reports whether any decoded string or key holds U+0000.
func jsonContainsNUL(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false // validateResult reports invalid JSON separately.
	}
	var walk func(any) bool
	walk = func(node any) bool {
		switch typed := node.(type) {
		case string:
			return strings.ContainsRune(typed, 0)
		case []any:
			for _, item := range typed {
				if walk(item) {
					return true
				}
			}
		case map[string]any:
			for key, item := range typed {
				if strings.ContainsRune(key, 0) || walk(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}
