//go:build integration

package agents_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestHeartbeatRuntimeAndGracefulShutdown(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agent_runtime")
	repo := testsupport.AgentRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{DisplayName: "Service host"}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := enroll(t, repo, token, "service-host")
	if err != nil {
		t.Fatal(err)
	}
	principal := agents.Principal{AgentID: agent.result.AgentID, CredentialID: agent.result.CredentialID}

	read := func() agents.Agent {
		t.Helper()
		item, err := repo.Get(ctx, principal.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	if item := read(); item.Connection != agents.ConnectionNeverSeen || item.Online {
		t.Fatalf("fresh agent: %s online=%v", item.Connection, item.Online)
	}

	// Agents released before runtime reporting still heartbeat normally.
	if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("service-host")}); err != nil {
		t.Fatal(err)
	}
	if item := read(); item.Connection != agents.ConnectionOnline || item.RunMode != "" {
		t.Fatalf("legacy heartbeat: %s %q", item.Connection, item.RunMode)
	}

	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	boot := time.Now().Add(-time.Hour).Truncate(time.Second)
	runtime := &agentproto.AgentRuntime{RunMode: agentproto.RunModeWindowsService, StartedAt: started.Unix(), HostBootAt: boot.Unix()}
	if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("service-host"), Runtime: runtime}); err != nil {
		t.Fatal(err)
	}
	item := read()
	if item.RunMode != agentproto.RunModeWindowsService || item.ProcessStartedAt == nil || !item.ProcessStartedAt.Equal(started) ||
		item.HostBootAt == nil || !item.HostBootAt.Equal(boot) || item.Connection != agents.ConnectionOnline {
		t.Fatalf("runtime not recorded: %+v", item)
	}

	// A graceful stop is visible immediately, not only after the online window.
	stopping := *runtime
	stopping.Stopping = true
	if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("service-host"), Runtime: &stopping}); err != nil {
		t.Fatal(err)
	}
	if item := read(); item.Online || item.Connection != agents.ConnectionStopped || item.LastShutdownAt == nil {
		t.Fatalf("stopping heartbeat: online=%v connection=%s", item.Online, item.Connection)
	}

	// The next start brings it back online.
	restarted := *runtime
	restarted.StartedAt = time.Now().Unix()
	if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("service-host"), Runtime: &restarted}); err != nil {
		t.Fatal(err)
	}
	if item := read(); !item.Online || item.Connection != agents.ConnectionOnline {
		t.Fatalf("restarted agent: online=%v connection=%s", item.Online, item.Connection)
	}

	// Silence beyond the online window is "offline", not "stopped".
	if _, err := pool.Exec(ctx, `UPDATE agents SET last_seen_at = now() - interval '10 minutes', last_shutdown_at = now() - interval '20 minutes' WHERE id = $1::uuid`, principal.AgentID); err != nil {
		t.Fatal(err)
	}
	if item := read(); item.Online || item.Connection != agents.ConnectionOffline {
		t.Fatalf("silent agent: online=%v connection=%s", item.Online, item.Connection)
	}

	// Invalid runtime reports are rejected.
	for _, bad := range []agentproto.AgentRuntime{
		{RunMode: "launchd", StartedAt: started.Unix()},
		{RunMode: agentproto.RunModeSystemd, StartedAt: time.Now().Add(time.Hour).Unix()},
		{RunMode: agentproto.RunModeSystemd},
	} {
		if err := repo.Heartbeat(ctx, principal, agentproto.HeartbeatRequest{Host: facts("service-host"), Runtime: &bad}); !errors.Is(err, agents.ErrInvalidInput) {
			t.Fatalf("runtime %+v accepted: %v", bad, err)
		}
	}
}
