package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// testHelperServer serves agent-1 with the test Core key and a no-op handler
// for system.reboot that counts its runs.
func testHelperServer(t *testing.T, replayPath string) (*helperServer, *int) {
	t.Helper()
	pin, err := parseHelperPin([]byte(`{"version":1,"agentId":"agent-1","coreKeys":["` + base64.StdEncoding.EncodeToString(testCorePublic) + `"]}`))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := openReplayStore(replayPath)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	server := &helperServer{pin: pin, replay: replay, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now,
		authorized: func(net.Conn) error { return nil },
		handlers: map[string]collector{agentproto.CapabilitySystemReboot: func(context.Context, map[string]any) (map[string]any, error) {
			runs++
			return map[string]any{"noop": true}, nil
		}}}
	return server, &runs
}

func runRequest(command agentproto.Command) helperRequest {
	return helperRequest{Op: "run", Command: &command}
}

func TestHelperRunsAnApprovedCommandOnce(t *testing.T) {
	replayPath := filepath.Join(t.TempDir(), helperReplayFileName)
	server, runs := testHelperServer(t, replayPath)
	command := approvedRebootCommand("c1", testCoreKey, time.Now())
	if response := server.handle(context.Background(), runRequest(command)); response.Code != "" || response.Output["noop"] != true {
		t.Fatalf("first run: %+v", response)
	}
	if response := server.handle(context.Background(), runRequest(command)); response.Code != "replayed" {
		t.Fatalf("replay: %+v", response)
	}
	// The register survives a helper restart.
	restarted, _ := testHelperServer(t, replayPath)
	if response := restarted.handle(context.Background(), runRequest(command)); response.Code != "replayed" {
		t.Fatalf("replay after restart: %+v", response)
	}
	if *runs != 1 {
		t.Fatalf("handler ran %d times", *runs)
	}
}

func TestHelperRefusals(t *testing.T) {
	_, otherKey, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	cases := map[string]struct {
		command agentproto.Command
		code    string
	}{
		"missing approval": {func() agentproto.Command {
			c := approvedRebootCommand("m1", testCoreKey, now)
			c.Approval = nil
			return c
		}(), "approval_rejected"},
		"expired approval": {approvedRebootCommand("m2", testCoreKey, now.Add(-agentproto.DefaultApprovalTTL-time.Minute)), "approval_rejected"},
		"other key":        {approvedRebootCommand("m3", otherKey, now), "approval_rejected"},
		"tampered parameters": {func() agentproto.Command {
			c := approvedRebootCommand("m4", testCoreKey, now)
			c.Parameters = map[string]any{"force": true}
			return c
		}(), "approval_rejected"},
		"tampered signature": {func() agentproto.Command {
			c := approvedRebootCommand("m5", testCoreKey, now)
			c.Approval.ApprovedBy = "someone-else"
			return c
		}(), "approval_rejected"},
		"other agent": {func() agentproto.Command {
			c := approvedRebootCommand("m6", testCoreKey, now)
			c.AgentID = "agent-2"
			approval := agentproto.SignCommandApproval(testCoreKey, agentproto.CommandApproval{CommandID: c.ID, AgentID: "agent-2", Type: c.Type,
				ParametersDigest: agentproto.ParametersDigest(c.Parameters), ApprovedAt: now, ExpiresAt: now.Add(agentproto.DefaultApprovalTTL)})
			c.Approval = &approval
			return c
		}(), "agent_mismatch"},
		"not allowlisted": {func() agentproto.Command {
			c := approvedRebootCommand("m7", testCoreKey, now)
			c.Type = agentproto.CapabilityScriptRun
			approval := agentproto.SignCommandApproval(testCoreKey, agentproto.CommandApproval{CommandID: c.ID, AgentID: c.AgentID, Type: c.Type,
				ParametersDigest: agentproto.ParametersDigest(c.Parameters), ApprovedAt: now, ExpiresAt: now.Add(agentproto.DefaultApprovalTTL)})
			c.Approval = &approval
			return c
		}(), "not_allowlisted"},
		"deadline passed": {func() agentproto.Command {
			c := approvedRebootCommand("m8", testCoreKey, now)
			c.Deadline = now.Add(-time.Second)
			return c
		}(), "deadline_passed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
			if response := server.handle(context.Background(), runRequest(tc.command)); response.Code != tc.code {
				t.Fatalf("code %q, want %q (%s)", response.Code, tc.code, response.Reason)
			}
			if *runs != 0 {
				t.Fatal("handler ran")
			}
		})
	}
}

func TestHelperProductionAllowlist(t *testing.T) {
	// The allowlist is exactly the system-management commands (#92); shell,
	// scripts and files stay with MeshCentral (decision 0006).
	want := []string{agentproto.CapabilityProcessTerminate, agentproto.CapabilityServiceRestart, agentproto.CapabilityServiceStop,
		agentproto.CapabilitySystemReboot, agentproto.CapabilitySystemShutdown}
	server := &helperServer{handlers: helperHandlers}
	if got := server.types(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allowlist = %v, want %v", got, want)
	}
	for _, name := range want {
		if info, ok := agentproto.LookupCapability(name); !ok || info.Risk != agentproto.RiskPrivileged {
			t.Errorf("%s is not a catalogued privileged capability", name)
		}
	}
}

func TestSystemCommandsRevalidateParameters(t *testing.T) {
	for commandType, parameters := range map[string]map[string]any{
		agentproto.CapabilityServiceRestart:   {"name": "--now"},
		agentproto.CapabilityServiceStop:      {},
		agentproto.CapabilityProcessTerminate: {"pid": 1.0, "name": "init"},
		agentproto.CapabilitySystemReboot:     {"force": true},
	} {
		_, err := helperHandlers[commandType](context.Background(), parameters)
		var refusal *helperError
		if !errors.As(err, &refusal) || refusal.Code != "invalid_parameters" {
			t.Errorf("%s %v: err = %v", commandType, parameters, err)
		}
	}
}

// pipeClient connects a helperClient to server over an in-memory connection.
func pipeClient(server *helperServer, agentID string) *helperClient {
	return &helperClient{agentID: agentID, dial: func(context.Context) (net.Conn, error) {
		client, peer := net.Pipe()
		go server.handleConn(context.Background(), peer)
		return client, nil
	}}
}

func TestHelperClientRoundTrip(t *testing.T) {
	server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	client := pipeClient(server, "agent-1")
	if types := client.types(); len(types) != 1 || types[0] != agentproto.CapabilitySystemReboot {
		t.Fatalf("types = %v", types)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := client.run(ctx, approvedRebootCommand("rt1", testCoreKey, time.Now()))
	if err != nil || output["noop"] != true || *runs != 1 {
		t.Fatalf("output=%v err=%v runs=%d", output, err, *runs)
	}
	_, err = client.run(ctx, approvedRebootCommand("rt1", testCoreKey, time.Now()))
	var refusal *helperError
	if !errors.As(err, &refusal) || refusal.Code != "replayed" {
		t.Fatalf("replay err = %v", err)
	}
}

func TestHelperClientIgnoresAHelperForAnotherAgent(t *testing.T) {
	server, _ := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	if types := pipeClient(server, "agent-9").types(); len(types) != 0 {
		t.Fatalf("types = %v", types)
	}
}

func TestHelperRefusesUnauthorizedCallers(t *testing.T) {
	server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	server.authorized = func(net.Conn) error { return errors.New("uid 1234") }
	_, err := pipeClient(server, "agent-1").run(context.Background(), approvedRebootCommand("u1", testCoreKey, time.Now()))
	var refusal *helperError
	if !errors.As(err, &refusal) || refusal.Code != "helper_unavailable" || *runs != 0 {
		t.Fatalf("err=%v runs=%d", err, *runs)
	}
}

func TestHelperPinValidation(t *testing.T) {
	for name, raw := range map[string]string{
		"not JSON":    `{`,
		"no agent":    `{"version":1,"coreKeys":["` + base64.StdEncoding.EncodeToString(testCorePublic) + `"]}`,
		"no keys":     `{"version":1,"agentId":"agent-1","coreKeys":[]}`,
		"bad key":     `{"version":1,"agentId":"agent-1","coreKeys":["AAAA"]}`,
		"old version": `{"version":0,"agentId":"agent-1","coreKeys":["` + base64.StdEncoding.EncodeToString(testCorePublic) + `"]}`,
	} {
		if _, err := parseHelperPin([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReplayStoreRefusesWhenFull(t *testing.T) {
	store, err := openReplayStore(filepath.Join(t.TempDir(), helperReplayFileName))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for index := range helperReplayCapacity {
		store.entries[strconv.Itoa(index)] = now.Add(time.Minute)
	}
	var refusal *helperError
	if err := store.claim("next", now.Add(time.Minute), now); !errors.As(err, &refusal) || refusal.Code != "helper_busy" {
		t.Fatalf("claim on a full register: %v", err)
	}
	// Entries well past their approval's lifetime are pruned.
	if err := store.claim("later", now.Add(3*time.Hour), now.Add(3*time.Hour)); err != nil {
		t.Fatalf("claim after expiry: %v", err)
	}
}
