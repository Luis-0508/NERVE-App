package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

func TestEncodeOutputTruncatesLists(t *testing.T) {
	items := make([]map[string]any, 200)
	for index := range items {
		items[index] = map[string]any{"name": strings.Repeat("x", 100), "index": index}
	}
	value := map[string]any{"items": items, "total": len(items), "returned": len(items)}
	encoded, truncated, err := encodeOutput(value, 4096)
	if err != nil || !truncated || len(encoded) > 4096 {
		t.Fatalf("truncation failed: %d bytes, truncated=%v, err=%v", len(encoded), truncated, err)
	}
	var decoded struct {
		Returned  int  `json:"returned"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	if json.Unmarshal(encoded, &decoded) != nil || decoded.Total != 200 || decoded.Returned == 0 || decoded.Returned >= 200 || !decoded.Truncated {
		t.Fatalf("truncated output metadata wrong: %+v", decoded)
	}
	if _, _, err := encodeOutput(map[string]any{"blob": strings.Repeat("y", 5000)}, 1024); err == nil {
		t.Fatal("oversized non-list output was accepted")
	}
	small, truncated, _ := encodeOutput(map[string]any{"ok": true}, 1024)
	if truncated || string(small) != `{"ok":true}` {
		t.Fatal("small output was modified")
	}
}

type recordedCall struct {
	route string
	body  []byte
}

func commandTestManager(t *testing.T, handler func(route string, body []byte) (int, any)) (*manager, *[]recordedCall, func()) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(nil)
	var (
		mu    sync.Mutex
		calls []recordedCall
	)
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		mu.Lock()
		calls = append(calls, recordedCall{route, body})
		mu.Unlock()
		return handler(route, body)
	})
	m := &manager{client: newManagementClient(server.URL+"/api/v1", &identity{CredentialID: "cred", key: private}), agentID: "agent-1",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), capabilities: func() []string { return []string{"system.info"} },
		facts: collectHostFacts, collectors: map[string]collector{
			"system.info": func(ctx context.Context, _ map[string]any) (map[string]any, error) {
				return map[string]any{"hostname": "test"}, nil
			},
		}}
	return m, &calls, server.Close
}

func routes(calls []recordedCall) string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.route)
	}
	return strings.Join(names, ",")
}

func TestExecuteRunsOnceAndReportsResult(t *testing.T) {
	m, calls, closeServer := commandTestManager(t, func(route string, body []byte) (int, any) {
		if strings.HasSuffix(route, "/start") {
			return 204, nil
		}
		return 200, agentproto.ResultAck{Status: "succeeded"}
	})
	defer closeServer()
	command := agentproto.Command{ID: "c1", AgentID: "agent-1", Type: "system.info", TimeoutSeconds: 5, MaxOutputBytes: 1024, Deadline: time.Now().Add(5 * time.Second)}
	m.execute(context.Background(), command)
	m.execute(context.Background(), command) // A re-delivered ID must not run again.
	if got := routes(*calls); got != "/agent/commands/c1/start,/agent/commands/c1/result" {
		t.Fatalf("unexpected calls: %s", got)
	}
	var result agentproto.CommandResult
	if err := json.Unmarshal((*calls)[1].body, &result); err != nil || result.Status != "succeeded" || string(result.Output) != `{"hostname":"test"}` {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
}

func TestExecuteRefusesUnsafeCommands(t *testing.T) {
	m, calls, closeServer := commandTestManager(t, func(route string, body []byte) (int, any) {
		if strings.HasSuffix(route, "/start") {
			return 409, map[string]any{"error": map[string]string{"code": agentproto.CodeCommandCancelled}}
		}
		return 200, agentproto.ResultAck{}
	})
	defer closeServer()
	future := time.Now().Add(5 * time.Second)
	// Addressed to another agent: nothing happens.
	m.execute(context.Background(), agentproto.Command{ID: "x1", AgentID: "agent-2", Type: "system.info", Deadline: future})
	// Unsupported capability: reported as failed without running anything.
	m.execute(context.Background(), agentproto.Command{ID: "x2", AgentID: "agent-1", Type: "command.shell", Deadline: future})
	// Expired before start: reported as timed out.
	m.execute(context.Background(), agentproto.Command{ID: "x3", AgentID: "agent-1", Type: "system.info", Deadline: time.Now().Add(-time.Second)})
	// Cancelled before start: the collector never runs and no result is sent.
	m.execute(context.Background(), agentproto.Command{ID: "x4", AgentID: "agent-1", Type: "system.info", TimeoutSeconds: 5, MaxOutputBytes: 1024, Deadline: future})
	if got := routes(*calls); got != "/agent/commands/x2/result,/agent/commands/x3/result,/agent/commands/x4/start" {
		t.Fatalf("unexpected calls: %s", got)
	}
	var unsupported, expired agentproto.CommandResult
	_ = json.Unmarshal((*calls)[0].body, &unsupported)
	_ = json.Unmarshal((*calls)[1].body, &expired)
	if unsupported.FailureCode != "unsupported_capability" || expired.Status != agentproto.CommandTimedOut {
		t.Fatalf("unexpected failure results: %+v %+v", unsupported, expired)
	}
}

func TestExecuteEnforcesTimeout(t *testing.T) {
	m, calls, closeServer := commandTestManager(t, func(route string, body []byte) (int, any) {
		if strings.HasSuffix(route, "/start") {
			return 204, nil
		}
		return 200, agentproto.ResultAck{}
	})
	defer closeServer()
	m.collectors["system.info"] = func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	started := time.Now()
	m.execute(context.Background(), agentproto.Command{ID: "slow", AgentID: "agent-1", Type: "system.info", TimeoutSeconds: 1, MaxOutputBytes: 1024, Deadline: time.Now().Add(time.Minute)})
	if time.Since(started) > 5*time.Second {
		t.Fatal("timeout was not enforced")
	}
	var result agentproto.CommandResult
	_ = json.Unmarshal((*calls)[len(*calls)-1].body, &result)
	if result.Status != agentproto.CommandTimedOut || result.FailureCode != "timeout" {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
}

func TestDisabledCapabilitiesAreNotAdvertised(t *testing.T) {
	t.Setenv("NERVE_AGENT_DISABLED_CAPABILITIES", "system.info, process.list")
	for _, name := range supportedCapabilities() {
		if name == "system.info" || name == "process.list" {
			t.Fatalf("disabled capability %s advertised", name)
		}
	}
}

func TestNativeCollectorsProduceBoundedJSON(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("collectors are implemented for Linux and Windows")
	}
	for _, name := range platformCapabilities() {
		collect, _ := collectorFor(name)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		output, err := collect(ctx, map[string]any{"limit": 50.0})
		cancel()
		if errors.Is(err, errUnsupported) {
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		encoded, _, err := encodeOutput(output, 1<<20)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("%s produced invalid output: %v", name, err)
		}
		if name == agentproto.CapabilityProcessList && (output["returned"].(int) == 0 || output["returned"].(int) > 50) {
			t.Fatalf("process list not bounded: %v", output["returned"])
		}
		if strings.Contains(string(encoded), "cmdline") || strings.Contains(string(encoded), "environ") {
			t.Fatalf("%s collected command lines or environment", name)
		}
	}
}
