//go:build integration

package remote_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/redact"
	"github.com/nerve-labs/nerve/internal/remote"
)

func TestResultRedactionAndRetention(t *testing.T) {
	f := newFixture(t, "remote_redaction")
	ctx := context.Background()
	a := f.agent(t, "system.info", "process.list")
	run := func(result agentproto.CommandResult) (string, agentproto.ResultAck, error) {
		t.Helper()
		command := f.create(t, a.AgentID, "system.info", nil)
		if _, err := f.commands.Claim(ctx, a, 0); err != nil {
			t.Fatal(err)
		}
		ack, err := f.commands.SubmitResult(ctx, a, command.ID, result)
		return command.ID, ack, err
	}

	// An agent that did not redact (outdated or modified) still cannot store
	// the secret: Core applies the rules again and counts what it replaced.
	raw := agentproto.CommandResult{Status: agentproto.CommandSucceeded,
		Output: []byte(`{"commandLine":"backup --password=hunter2 --target https://svc:pa55@nas/share"}`)}
	unredacted, _, err := run(raw)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := f.commands.Get(ctx, unredacted)
	if err != nil || stored.Result == nil {
		t.Fatalf("stored result: %+v %v", stored, err)
	}
	if strings.Contains(string(stored.Result.Output), "hunter2") || strings.Contains(string(stored.Result.Output), "pa55") ||
		!maps.Equal(stored.Result.Redactions, redact.Counts{redact.KindPassword: 1, redact.KindURLCredentials: 1}) {
		t.Fatalf("secret stored or not counted: %s %v", stored.Result.Output, stored.Result.Redactions)
	}

	// The agent-side redaction yields the same digest Core stores, so a
	// retried submission of the redacted result is acknowledged as a duplicate.
	redacted := raw
	redacted.Output = append([]byte(nil), raw.Output...)
	redacted.Redact()
	if ack, err := f.commands.SubmitResult(ctx, a, unredacted, redacted); err != nil || !ack.Duplicate {
		t.Fatalf("redacted resubmission: %+v %v", ack, err)
	}
	if ack, err := f.commands.SubmitResult(ctx, a, unredacted, raw); err != nil || !ack.Duplicate {
		t.Fatalf("raw resubmission: %+v %v", ack, err)
	}
	var auditRedactions string
	if err := f.pool.QueryRow(ctx, `SELECT safe_parameters->>'redactions' FROM audit_log WHERE action = 'remote.command.completed' AND target_id = $1`,
		unredacted).Scan(&auditRedactions); err != nil || !strings.Contains(auditRedactions, `"password": 1`) {
		t.Fatalf("audit redactions = %q (err %v)", auditRedactions, err)
	}

	// Counts the agent reports are bounded to known kinds.
	for _, invalid := range []redact.Counts{{"made_up": 1}, {redact.KindPassword: -1}, {redact.KindPassword: 2_000_000}} {
		result := agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: []byte(`{}`), Redactions: invalid}
		if _, _, err := run(result); !errors.Is(err, remote.ErrInvalidResult) {
			t.Fatalf("redaction counts %v: %v", invalid, err)
		}
		// Leave no claimed command behind for the next iteration.
		if _, err := f.pool.Exec(ctx, `UPDATE remote_commands SET status = 'timed_out', completed_at = now(), failure_code = 'no_result'
			WHERE agent_id = $1::uuid AND status = 'claimed'`, a.AgentID); err != nil {
			t.Fatal(err)
		}
	}

	// Retention removes only results older than the configured period and
	// keeps every command row and its audit trail.
	recent, _, err := run(agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: []byte(`{"uptime":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	// Results are write-once; backdating one needs the trigger off briefly.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE remote_command_results DISABLE TRIGGER remote_command_results_write_once;
		UPDATE remote_command_results SET submitted_at = now() - interval '3 hours' WHERE command_id = '`+unredacted+`'::uuid;
		ALTER TABLE remote_command_results ENABLE TRIGGER remote_command_results_write_once`); err != nil {
		t.Fatal(err)
	}
	var commandsBefore int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM remote_commands`).Scan(&commandsBefore)
	f.commands.SetResultRetention(2 * time.Hour)
	if err := f.commands.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if command, _ := f.commands.Get(ctx, unredacted); command.Result != nil || !command.ResultPruned || command.Status != "succeeded" {
		t.Fatalf("expired result not pruned: %+v", command)
	}
	if command, _ := f.commands.Get(ctx, recent); command.Result == nil {
		t.Fatal("recent result was pruned")
	}
	var commandsAfter int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM remote_commands`).Scan(&commandsAfter)
	if commandsAfter != commandsBefore || f.auditCount(t, "remote.command.completed", unredacted) != 1 {
		t.Fatalf("command history changed: %d -> %d commands", commandsBefore, commandsAfter)
	}
}
