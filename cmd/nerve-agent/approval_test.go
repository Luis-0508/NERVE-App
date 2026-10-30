package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// privilegedTestManager runs a manager against a fake Core over HTTPS (or
// plain HTTP) with a privileged test collector that records whether it ran.
func privilegedTestManager(t *testing.T, useTLS bool) (*manager, *[]recordedCall, *bool) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(nil)
	var (
		mu    sync.Mutex
		calls []recordedCall
		ran   bool
	)
	handler := fakeCoreHandler(&public, func(route string, body []byte) (int, any) {
		mu.Lock()
		calls = append(calls, recordedCall{route, body})
		mu.Unlock()
		if strings.HasSuffix(route, "/start") {
			return 204, nil
		}
		return 200, agentproto.ResultAck{Status: "succeeded"}
	})
	server := httptest.NewServer(handler)
	if useTLS {
		server.Close()
		server = httptest.NewTLSServer(handler)
	}
	t.Cleanup(server.Close)
	id := &identity{CredentialID: "cred", key: private, coreKey: testCorePublic}
	m := &manager{client: newManagementClientWith(server.URL+"/api/v1", id, server.Client().Transport), agentID: "agent-1",
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		capabilities: func() []string { return []string{agentproto.CapabilitySystemReboot} }, facts: collectHostFacts,
		collectors: map[string]collector{agentproto.CapabilitySystemReboot: func(context.Context, map[string]any) (map[string]any, error) {
			ran = true
			return map[string]any{"ok": true}, nil
		}}}
	return m, &calls, &ran
}

func approvedRebootCommand(id string, key ed25519.PrivateKey, now time.Time) agentproto.Command {
	command := agentproto.Command{ID: id, AgentID: "agent-1", Type: agentproto.CapabilitySystemReboot, Parameters: map[string]any{},
		TimeoutSeconds: 5, MaxOutputBytes: 1024, Deadline: now.Add(5 * time.Second)}
	approval := agentproto.SignCommandApproval(key, agentproto.CommandApproval{CommandID: id, AgentID: "agent-1", Type: command.Type,
		ParametersDigest: agentproto.ParametersDigest(command.Parameters), ApprovedAt: now, ExpiresAt: now.Add(agentproto.DefaultApprovalTTL)})
	command.Approval = &approval
	return command
}

func failureCode(t *testing.T, call recordedCall) string {
	t.Helper()
	var result agentproto.CommandResult
	if err := json.Unmarshal(call.body, &result); err != nil {
		t.Fatal(err)
	}
	return result.FailureCode
}

func TestPrivilegedCommandRunsWithValidApprovalOverHTTPS(t *testing.T) {
	m, calls, ran := privilegedTestManager(t, true)
	if m.client.transport() != agentproto.TransportHTTPS {
		t.Fatalf("transport = %q", m.client.transport())
	}
	command := approvedRebootCommand("p1", testCoreKey, time.Now())
	m.execute(context.Background(), command)
	m.execute(context.Background(), command) // A replayed delivery never runs twice.
	if !*ran || routes(*calls) != "/agent/commands/p1/start,/agent/commands/p1/result" {
		t.Fatalf("ran=%v calls=%s", *ran, routes(*calls))
	}
}

func TestPrivilegedCommandRefusals(t *testing.T) {
	_, otherKey, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	cases := map[string]struct {
		tls     bool
		command agentproto.Command
		code    string
	}{
		"plain HTTP": {tls: false, command: approvedRebootCommand("r1", testCoreKey, now), code: "insecure_transport"},
		"no approval": {tls: true, command: func() agentproto.Command {
			c := approvedRebootCommand("r2", testCoreKey, now)
			c.Approval = nil
			return c
		}(), code: "approval_rejected"},
		"wrong key": {tls: true, command: approvedRebootCommand("r3", otherKey, now), code: "approval_rejected"},
		"expired":   {tls: true, command: approvedRebootCommand("r4", testCoreKey, now.Add(-agentproto.DefaultApprovalTTL-time.Minute)), code: "approval_rejected"},
		"other command": {tls: true, command: func() agentproto.Command {
			c := approvedRebootCommand("r5", testCoreKey, now)
			c.ID = "r6"
			return c
		}(), code: "approval_rejected"},
		"tampered parameters": {tls: true, command: func() agentproto.Command {
			c := approvedRebootCommand("r7", testCoreKey, now)
			c.Parameters = map[string]any{"force": true}
			return c
		}(), code: "approval_rejected"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, calls, ran := privilegedTestManager(t, tc.tls)
			if tc.command.Deadline.Before(time.Now()) {
				tc.command.Deadline = time.Now().Add(5 * time.Second)
			}
			m.execute(context.Background(), tc.command)
			if *ran || len(*calls) != 1 || !strings.HasSuffix((*calls)[0].route, "/result") {
				t.Fatalf("ran=%v calls=%s", *ran, routes(*calls))
			}
			if code := failureCode(t, (*calls)[0]); code != tc.code {
				t.Fatalf("failure code %q, want %q", code, tc.code)
			}
		})
	}
}
