//go:build integration

package remote_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

type fixture struct {
	pool     *pgxpool.Pool
	agents   *agents.Repository
	commands *remote.Service
	actor    agents.Actor
}

func newFixture(t *testing.T, prefix string) fixture {
	pool := testsupport.MigratedPool(t, prefix)
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	return fixture{pool: pool, agents: agents.NewRepository(pool), commands: remote.NewService(pool), actor: agents.Actor{UserID: adminID}}
}

func (f fixture) agent(t *testing.T, capabilities ...string) agents.Principal {
	t.Helper()
	ctx := context.Background()
	_, token, err := f.agents.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	facts := agentproto.HostFacts{Hostname: "host", OS: "linux", Architecture: "amd64", AgentVersion: "test"}
	result, err := f.agents.Enroll(ctx, token, public, facts, "")
	if err != nil {
		t.Fatal(err)
	}
	principal := agents.Principal{AgentID: result.AgentID, CredentialID: result.CredentialID}
	if err := f.agents.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts, Capabilities: capabilities}); err != nil {
		t.Fatal(err)
	}
	return principal
}

func (f fixture) create(t *testing.T, agentID, commandType string, parameters map[string]any) remote.Command {
	t.Helper()
	command, err := f.commands.Create(context.Background(), remote.CreateInput{AgentID: agentID, Type: commandType, Parameters: parameters}, f.actor)
	if err != nil {
		t.Fatalf("create %s: %v", commandType, err)
	}
	return command
}

func (f fixture) auditCount(t *testing.T, action, commandID string) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2`, action, commandID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func succeeded(output string) agentproto.CommandResult {
	return agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: json.RawMessage(output)}
}

func TestCommandLifecycle(t *testing.T) {
	f := newFixture(t, "remote_lifecycle")
	ctx := context.Background()
	a := f.agent(t, "system.info", "process.list", "command.shell")
	b := f.agent(t, "system.info")

	// Validation and capability enforcement.
	for name, input := range map[string]remote.CreateInput{
		"privileged":    {AgentID: a.AgentID, Type: "command.shell"},
		"unknown":       {AgentID: a.AgentID, Type: "made.up"},
		"not offered":   {AgentID: b.AgentID, Type: "process.list"},
		"bad parameter": {AgentID: a.AgentID, Type: "process.list", Parameters: map[string]any{"limit": 5000.0}},
		"extra param":   {AgentID: a.AgentID, Type: "system.info", Parameters: map[string]any{"cmd": "whoami"}},
		"long timeout":  {AgentID: a.AgentID, Type: "system.info", TimeoutSeconds: 3600},
	} {
		_, err := f.commands.Create(ctx, input, f.actor)
		if err == nil {
			t.Fatalf("%s command was accepted", name)
		}
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: a.AgentID, Type: "command.shell"}, f.actor); !errors.Is(err, remote.ErrUnknownCommand) {
		t.Fatalf("privileged command: %v", err)
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: b.AgentID, Type: "process.list"}, f.actor); !errors.Is(err, remote.ErrCapabilityNotAdvertised) {
		t.Fatalf("unadvertised capability: %v", err)
	}

	command := f.create(t, a.AgentID, "process.list", map[string]any{"limit": 25.0})
	if command.Status != "queued" || command.Parameters["limit"] != 25.0 {
		t.Fatalf("unexpected queued command: %+v", command)
	}

	// Another agent never receives it.
	if _, err := f.commands.Claim(ctx, b, 0); !errors.Is(err, remote.ErrNoCommand) {
		t.Fatalf("foreign agent claim: %v", err)
	}
	claimed, err := f.commands.Claim(ctx, a, 0)
	if err != nil || claimed.ID != command.ID || claimed.AgentID != a.AgentID || claimed.Parameters["limit"] != 25.0 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if time.Until(claimed.Deadline) > time.Duration(claimed.TimeoutSeconds)*time.Second || claimed.MaxOutputBytes != 512<<10 {
		t.Fatalf("claim deadline/limits wrong: %+v", claimed)
	}
	if _, err := f.commands.Claim(ctx, a, 0); !errors.Is(err, remote.ErrNoCommand) {
		t.Fatalf("command delivered twice: %v", err)
	}

	if err := f.commands.Start(ctx, b, command.ID); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("foreign start: %v", err)
	}
	if err := f.commands.Start(ctx, a, command.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.commands.Start(ctx, a, command.ID); err != nil {
		t.Fatalf("repeated start: %v", err)
	}

	result := succeeded(`{"items":[{"pid":1,"name":"init"}],"returned":1}`)
	if _, err := f.commands.SubmitResult(ctx, b, command.ID, result); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("foreign result: %v", err)
	}
	var rejected int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'remote.command.result_rejected' AND actor_agent_id = $1::uuid`, b.AgentID).Scan(&rejected)
	if rejected != 1 {
		t.Fatal("foreign result attempt was not audited")
	}
	large := succeeded(`{"blob":"` + strings.Repeat("x", 600<<10) + `"}`)
	if _, err := f.commands.SubmitResult(ctx, a, command.ID, large); !errors.Is(err, remote.ErrResultTooLarge) {
		t.Fatalf("oversized result: %v", err)
	}
	for _, invalid := range []agentproto.CommandResult{
		{Status: "exploded"},
		{Status: agentproto.CommandFailed},
		{Status: agentproto.CommandSucceeded, Output: json.RawMessage(`"string"`)},
		{Status: agentproto.CommandFailed, FailureCode: "Bad Code"},
	} {
		if _, err := f.commands.SubmitResult(ctx, a, command.ID, invalid); !errors.Is(err, remote.ErrInvalidResult) {
			t.Fatalf("invalid result %+v: %v", invalid, err)
		}
	}
	ack, err := f.commands.SubmitResult(ctx, a, command.ID, result)
	if err != nil || ack.Status != "succeeded" || ack.Duplicate {
		t.Fatalf("result: %+v %v", ack, err)
	}
	// A retry with the same result after a lost response is acknowledged; a different one is refused.
	reformatted := succeeded(`{ "returned": 1, "items": [ {"name":"init","pid":1} ] }`)
	if ack, err := f.commands.SubmitResult(ctx, a, command.ID, reformatted); err != nil || !ack.Duplicate {
		t.Fatalf("duplicate result: %+v %v", ack, err)
	}
	if _, err := f.commands.SubmitResult(ctx, a, command.ID, succeeded(`{"items":[]}`)); !errors.Is(err, remote.ErrAlreadyCompleted) {
		t.Fatalf("conflicting result: %v", err)
	}
	stored, err := f.commands.Get(ctx, command.ID)
	if err != nil || stored.Status != "succeeded" || stored.Result == nil || !strings.Contains(string(stored.Result.Output), `"init"`) || stored.CompletedAt == nil {
		t.Fatalf("stored command: %+v %v", stored, err)
	}
	for _, action := range []string{"remote.command.created", "remote.command.claimed", "remote.command.started", "remote.command.completed"} {
		if f.auditCount(t, action, command.ID) != 1 {
			t.Fatalf("expected one %s audit record", action)
		}
	}
	// Output stays out of the immutable audit log.
	var leaked int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE safe_parameters::text LIKE '%init%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("command output leaked into the audit log")
	}
	if _, err := f.commands.Cancel(ctx, command.ID, f.actor); !errors.Is(err, remote.ErrNotActive) {
		t.Fatalf("cancel finished command: %v", err)
	}

	// Cancellation before execution: start and result are refused.
	cancelled := f.create(t, a.AgentID, "system.info", nil)
	if _, err := f.commands.Claim(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Cancel(ctx, cancelled.ID, f.actor); err != nil {
		t.Fatal(err)
	}
	if err := f.commands.Start(ctx, a, cancelled.ID); !errors.Is(err, remote.ErrCancelled) {
		t.Fatalf("start after cancel: %v", err)
	}
	if _, err := f.commands.SubmitResult(ctx, a, cancelled.ID, succeeded(`{}`)); !errors.Is(err, remote.ErrCancelled) {
		t.Fatalf("result after cancel: %v", err)
	}
	if f.auditCount(t, "remote.command.cancelled", cancelled.ID) != 1 {
		t.Fatal("cancellation not audited")
	}

	// A failed result carries its failure code.
	failing := f.create(t, a.AgentID, "system.info", nil)
	if _, err := f.commands.Claim(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.SubmitResult(ctx, a, failing.ID, agentproto.CommandResult{Status: agentproto.CommandTimedOut, FailureCode: "timeout", FailureReason: "slow"}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := f.commands.Get(ctx, failing.ID); stored.Status != "timed_out" || stored.FailureCode != "timeout" {
		t.Fatalf("failed command: %+v", stored)
	}
}

func TestConcurrentClaimsDeliverOnce(t *testing.T) {
	f := newFixture(t, "remote_race")
	ctx := context.Background()
	a := f.agent(t, "system.info")
	created := map[string]bool{}
	for i := 0; i < 5; i++ {
		created[f.create(t, a.AgentID, "system.info", nil).ID] = true
	}
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		claims = map[string]int{}
	)
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				command, err := f.commands.Claim(ctx, a, 0)
				if errors.Is(err, remote.ErrNoCommand) {
					return
				}
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				claims[command.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claims) != len(created) {
		t.Fatalf("claimed %d distinct commands, want %d", len(claims), len(created))
	}
	for id, count := range claims {
		if count != 1 || !created[id] {
			t.Fatalf("command %s claimed %d times", id, count)
		}
	}
}

func TestLongPollWakesOnNewCommand(t *testing.T) {
	f := newFixture(t, "remote_poll")
	a := f.agent(t, "system.info")
	done := make(chan agentproto.Command, 1)
	started := time.Now()
	go func() {
		command, err := f.commands.Claim(context.Background(), a, 10*time.Second)
		if err != nil {
			t.Errorf("long poll: %v", err)
		}
		done <- command
	}()
	time.Sleep(300 * time.Millisecond)
	created := f.create(t, a.AgentID, "system.info", nil)
	select {
	case command := <-done:
		if command.ID != created.ID || time.Since(started) > 3*time.Second {
			t.Fatalf("long poll returned %q after %s", command.ID, time.Since(started))
		}
	case <-time.After(8 * time.Second):
		t.Fatal("long poll was not woken by the new command")
	}
	empty := time.Now()
	if _, err := f.commands.Claim(context.Background(), a, time.Second); !errors.Is(err, remote.ErrNoCommand) || time.Since(empty) < 900*time.Millisecond {
		t.Fatalf("empty long poll returned early: %v", err)
	}
}

func TestExpiryTimeoutRevocationAndLimits(t *testing.T) {
	f := newFixture(t, "remote_sweep")
	ctx := context.Background()
	a := f.agent(t, "system.info", "system.metrics")

	// An unclaimed command past its expiry is expired by the sweeper and never delivered.
	var expiredID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO remote_commands (agent_id, command_type, risk, requested_by, requested_at, expires_at, timeout_seconds, max_output_bytes)
		VALUES ($1::uuid, 'system.info', 'read_only', $2::uuid, now() - interval '1 hour', now() - interval '30 minutes', 20, 16384) RETURNING id::text`,
		a.AgentID, f.actor.UserID).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Claim(ctx, a, 0); !errors.Is(err, remote.ErrNoCommand) {
		t.Fatalf("expired command delivered: %v", err)
	}
	// A claimed command whose agent never reports times out.
	var staleID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO remote_commands (agent_id, command_type, risk, requested_by, requested_at, expires_at, timeout_seconds, max_output_bytes)
		VALUES ($1::uuid, 'system.info', 'read_only', $2::uuid, now() - interval '1 hour', now() + interval '1 hour', 20, 16384) RETURNING id::text`,
		a.AgentID, f.actor.UserID).Scan(&staleID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE remote_commands SET status = 'claimed', claimed_at = now() - interval '10 minutes', claimed_by_credential = $2::uuid WHERE id = $1::uuid`,
		staleID, a.CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := f.commands.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if command, _ := f.commands.Get(ctx, expiredID); command.Status != "expired" || f.auditCount(t, "remote.command.expired", expiredID) != 1 {
		t.Fatalf("expired command: %+v", command)
	}
	if command, _ := f.commands.Get(ctx, staleID); command.Status != "timed_out" || command.FailureCode != "no_result" || f.auditCount(t, "remote.command.timed_out", staleID) != 1 {
		t.Fatalf("stale command: %+v", command)
	}
	if _, err := f.commands.SubmitResult(ctx, a, staleID, succeeded(`{}`)); !errors.Is(err, remote.ErrAlreadyCompleted) {
		t.Fatalf("late result after timeout: %v", err)
	}

	// Per-agent queue limit.
	for i := 0; i < remote.MaxActivePerAgent; i++ {
		f.create(t, a.AgentID, "system.metrics", nil)
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: a.AgentID, Type: "system.info"}, f.actor); !errors.Is(err, remote.ErrQueueFull) {
		t.Fatalf("queue limit: %v", err)
	}

	// Revocation cancels everything pending in the same transaction.
	if _, err := f.agents.Revoke(ctx, a.AgentID, f.actor, f.commands.CancelForAgent); err != nil {
		t.Fatal(err)
	}
	var active, cancelled int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued','claimed','running')), count(*) FILTER (WHERE status = 'cancelled' AND failure_code = 'agent_revoked')
		FROM remote_commands WHERE agent_id = $1::uuid`, a.AgentID).Scan(&active, &cancelled)
	if active != 0 || cancelled != remote.MaxActivePerAgent {
		t.Fatalf("revocation left %d active commands (%d cancelled)", active, cancelled)
	}
	if _, err := f.commands.Create(ctx, remote.CreateInput{AgentID: a.AgentID, Type: "system.info"}, f.actor); !errors.Is(err, remote.ErrAgentUnavailable) {
		t.Fatalf("command for revoked agent: %v", err)
	}
}

func TestCommandInvariantsAreEnforcedByPostgres(t *testing.T) {
	f := newFixture(t, "remote_db")
	ctx := context.Background()
	a := f.agent(t, "system.info")
	command := f.create(t, a.AgentID, "system.info", nil)
	if _, err := f.commands.Claim(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.SubmitResult(ctx, a, command.ID, succeeded(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	queued := f.create(t, a.AgentID, "system.info", nil)
	for name, statement := range map[string]string{
		"reopen finished command":   `UPDATE remote_commands SET status = 'queued', claimed_at = NULL, claimed_by_credential = NULL, started_at = NULL, completed_at = NULL WHERE id = '` + command.ID + `'`,
		"rewrite parameters":        `UPDATE remote_commands SET parameters = '{"x":1}' WHERE id = '` + queued.ID + `'`,
		"retarget agent":            `UPDATE remote_commands SET agent_id = gen_random_uuid() WHERE id = '` + queued.ID + `'`,
		"skip to success":           `UPDATE remote_commands SET status = 'succeeded', completed_at = now() WHERE id = '` + queued.ID + `'`,
		"delete command":            `DELETE FROM remote_commands WHERE id = '` + command.ID + `'`,
		"rewrite result":            `UPDATE remote_command_results SET output = '{"forged":true}' WHERE command_id = '` + command.ID + `'`,
		"oversized parameters":      `INSERT INTO remote_commands (agent_id, command_type, risk, parameters, requested_by, expires_at, timeout_seconds, max_output_bytes) VALUES ('` + a.AgentID + `', 'system.info', 'read_only', jsonb_build_object('x', repeat('y', 5000)), '` + f.actor.UserID + `', now() + interval '1 minute', 10, 2048)`,
		"claimed without timestamp": `UPDATE remote_commands SET status = 'claimed' WHERE id = '` + queued.ID + `'`,
	} {
		if _, err := f.pool.Exec(ctx, statement); err == nil {
			t.Fatalf("%s was accepted by PostgreSQL", name)
		}
	}
}

func TestReviewRegressions(t *testing.T) {
	f := newFixture(t, "remote_review")
	ctx := context.Background()
	a := f.agent(t, "system.info", "system.metrics")

	// Concurrent creates cannot exceed the per-agent active limit.
	var wg sync.WaitGroup
	for i := 0; i < remote.MaxActivePerAgent+10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.commands.Create(ctx, remote.CreateInput{AgentID: a.AgentID, Type: "system.metrics"}, f.actor)
		}()
	}
	wg.Wait()
	var active int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM remote_commands WHERE agent_id = $1::uuid AND status = 'queued'`, a.AgentID).Scan(&active)
	if active != remote.MaxActivePerAgent {
		t.Fatalf("concurrent creates produced %d active commands, limit %d", active, remote.MaxActivePerAgent)
	}
	for {
		command, err := f.commands.Claim(ctx, a, 0)
		if errors.Is(err, remote.ErrNoCommand) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// NUL characters and Core-reserved failure codes are client errors, not 500s.
		for _, invalid := range []agentproto.CommandResult{
			succeeded(`{"name":"bad\u0000name"}`),
			{Status: agentproto.CommandFailed, FailureCode: "collection_failed", FailureReason: "x\x00y"},
			{Status: agentproto.CommandTimedOut, FailureCode: "no_result"},
		} {
			if _, err := f.commands.SubmitResult(ctx, a, command.ID, invalid); !errors.Is(err, remote.ErrInvalidResult) {
				t.Fatalf("invalid result %+v: %v", invalid, err)
			}
		}
		if _, err := f.commands.SubmitResult(ctx, a, command.ID, agentproto.CommandResult{Status: agentproto.CommandTimedOut, FailureCode: "timeout"}); err != nil {
			t.Fatal(err)
		}
	}

	// A pruned agent-submitted timed_out result is reported as pruned.
	var timedOut string
	_ = f.pool.QueryRow(ctx, `SELECT id::text FROM remote_commands WHERE agent_id = $1::uuid AND status = 'timed_out' LIMIT 1`, a.AgentID).Scan(&timedOut)
	if _, err := f.pool.Exec(ctx, `DELETE FROM remote_command_results WHERE command_id = $1::uuid`, timedOut); err != nil {
		t.Fatal(err)
	}
	if command, _ := f.commands.Get(ctx, timedOut); !command.ResultPruned {
		t.Fatalf("pruned timed_out result not flagged: %+v", command)
	}
}
