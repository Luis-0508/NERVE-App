//go:build integration

package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestRemoteCommandLifecycleOverHTTP(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_commands")
	testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	testsupport.CreateUser(t, pool, "viewer@example.invalid", "integration-password", "read_only")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")
	viewer := login(t, server.URL, "viewer@example.invalid", "integration-password")

	var created struct {
		Token string `json:"token"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{}`, nil), &created)
	agent := enrollTestAgent(t, server.URL, created.Token, "managed-host")
	heartbeat, _ := json.Marshal(agentproto.HeartbeatRequest{Capabilities: []string{"system.info", "command.shell"},
		Host: agentproto.HostFacts{Hostname: "managed-host", OS: "linux", Architecture: "amd64", AgentVersion: "test"}})
	if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/heartbeat", heartbeat, agent.credentialID, nil)); status != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", status, body)
	}
	commandsURL := server.URL + "/api/v1/agents/" + agent.agentID + "/commands"

	// Authorization: viewers cannot execute; privileged commands are not enabled for anyone.
	for client, expected := range map[*http.Client]int{viewer: http.StatusForbidden} {
		response := doJSON(t, client, http.MethodPost, commandsURL, `{"type":"system.info"}`, nil)
		if response.StatusCode != expected {
			t.Fatalf("viewer command status = %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}
	shell := doJSON(t, admin, http.MethodPost, commandsURL, `{"type":"command.shell","parameters":{"command":"id"}}`, nil)
	if shell.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("shell command status = %d", shell.StatusCode)
	}
	_ = shell.Body.Close()
	viewerList := doJSON(t, viewer, http.MethodGet, commandsURL, "", nil)
	if viewerList.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer command history status = %d", viewerList.StatusCode)
	}
	_ = viewerList.Body.Close()

	// A technician requests system.info.
	createdCommand := doJSON(t, technician, http.MethodPost, commandsURL, `{"type":"system.info"}`, nil)
	if createdCommand.StatusCode != http.StatusCreated {
		t.Fatalf("create command = %d: %s", createdCommand.StatusCode, readBody(createdCommand))
	}
	var command struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	decodeResponse(t, createdCommand, &command)

	// The agent claims, starts and reports.
	status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{"waitSeconds":2}`), agent.credentialID, nil))
	var claimed agentproto.Command
	if status != http.StatusOK || json.Unmarshal([]byte(body), &claimed) != nil || claimed.ID != command.ID || claimed.Type != "system.info" {
		t.Fatalf("claim = %d: %s", status, body)
	}
	if status, _ := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{"waitSeconds":0}`), agent.credentialID, nil)); status != http.StatusNoContent {
		t.Fatalf("second claim = %d", status)
	}
	if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/"+claimed.ID+"/start", []byte(`{}`), agent.credentialID, nil)); status != http.StatusNoContent {
		t.Fatalf("start = %d: %s", status, body)
	}
	result, _ := json.Marshal(agentproto.CommandResult{Status: "succeeded", Output: json.RawMessage(`{"hostname":"managed-host","uptimeSeconds":42}`)})
	if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/"+claimed.ID+"/result", result, agent.credentialID, nil)); status != http.StatusOK {
		t.Fatalf("result = %d: %s", status, body)
	}
	if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/"+claimed.ID+"/result", result, agent.credentialID, nil)); status != http.StatusOK || !strings.Contains(body, `"duplicate":true`) {
		t.Fatalf("duplicate result = %d: %s", status, body)
	}

	detail := readBody(doJSON(t, technician, http.MethodGet, server.URL+"/api/v1/remote/commands/"+command.ID, "", nil))
	for _, expected := range []string{`"status":"succeeded"`, `"uptimeSeconds":42`, `"requestedByEmail":"tech@example.invalid"`} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("command detail lacks %s: %s", expected, detail)
		}
	}
	history := readBody(doJSON(t, technician, http.MethodGet, commandsURL, "", nil))
	if !strings.Contains(history, command.ID) || strings.Contains(history, "uptimeSeconds") {
		t.Fatalf("history must list commands without output: %s", history)
	}

	auditBody := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/audit?targetType=remote_command&targetId="+command.ID, "", nil))
	for _, action := range []string{"remote.command.created", "remote.command.claimed", "remote.command.started", "remote.command.completed"} {
		if !strings.Contains(auditBody, `"action":"`+action+`"`) {
			t.Fatalf("audit trail lacks %s: %s", action, auditBody)
		}
	}
	if !strings.Contains(auditBody, `"actorEmail":"tech@example.invalid"`) || !strings.Contains(auditBody, `"actorAgentId":"`+agent.agentID+`"`) || strings.Contains(auditBody, "managed-host") {
		t.Fatalf("audit attribution or output redaction wrong: %s", auditBody)
	}

	// Cancelling needs the execute permission; the agent's start is then refused.
	second := doJSON(t, admin, http.MethodPost, commandsURL, `{"type":"system.info"}`, nil)
	decodeResponse(t, second, &command)
	cancel := doJSON(t, viewer, http.MethodPost, server.URL+"/api/v1/remote/commands/"+command.ID+"/cancel", `{}`, nil)
	if cancel.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer cancel = %d", cancel.StatusCode)
	}
	_ = cancel.Body.Close()
	if body := readBody(doJSON(t, technician, http.MethodPost, server.URL+"/api/v1/remote/commands/"+command.ID+"/cancel", `{}`, nil)); !strings.Contains(body, `"status":"cancelled"`) {
		t.Fatalf("cancel: %s", body)
	}
	if status, _ := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{}`), agent.credentialID, nil)); status != http.StatusNoContent {
		t.Fatalf("cancelled command was delivered: %d", status)
	}
}
