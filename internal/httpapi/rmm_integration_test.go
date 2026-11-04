//go:build integration

package httpapi_test

import (
	"context"
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

// TestDeviceSnapshotsAndAudit covers the two read endpoints of the RMM device
// page (#93): the newest read-only result per type and the device audit trail.
func TestDeviceSnapshotsAndAudit(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_rmm")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	testsupport.CreateUser(t, pool, "viewer@example.invalid", "integration-password", "read_only")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour, CoreSigningKey: testsupport.CoreSigningKeyHex},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")
	viewer := login(t, server.URL, "viewer@example.invalid", "integration-password")

	// Two agents on two devices; only the first device is inspected.
	enroll := func(hostname string) (*testAgent, string) {
		var created struct {
			Token string `json:"token"`
		}
		decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{}`, nil), &created)
		agent := enrollTestAgent(t, server.URL, created.Token, hostname)
		heartbeat, _ := json.Marshal(agentproto.HeartbeatRequest{Capabilities: []string{"system.info", "process.list"},
			Host: agentproto.HostFacts{Hostname: hostname, OS: "linux", Architecture: "amd64", AgentVersion: "test"}})
		if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/heartbeat", heartbeat, agent.credentialID, nil)); status != http.StatusOK {
			t.Fatalf("heartbeat = %d: %s", status, body)
		}
		deviceID := testsupport.CreateDevice(t, pool, hostname, "", "")
		if _, err := pool.Exec(ctx, `UPDATE agents SET device_id = $2::uuid, association_status = 'confirmed', associated_at = now(), associated_by = $3::uuid
			WHERE id = $1::uuid`, agent.agentID, deviceID, adminID); err != nil {
			t.Fatal(err)
		}
		return agent, deviceID
	}
	run := func(agent *testAgent, commandType, output string) string {
		var command struct {
			ID string `json:"id"`
		}
		decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agents/"+agent.agentID+"/commands", `{"type":"`+commandType+`"}`, nil), &command)
		status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{"waitSeconds":2}`), agent.credentialID, nil))
		if status != http.StatusOK || !strings.Contains(body, command.ID) {
			t.Fatalf("claim = %d: %s", status, body)
		}
		if output != "" {
			result, _ := json.Marshal(agentproto.CommandResult{Status: "succeeded", Output: json.RawMessage(output)})
			if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/"+command.ID+"/result", result, agent.credentialID, nil)); status != http.StatusOK {
				t.Fatalf("result = %d: %s", status, body)
			}
		}
		return command.ID
	}
	agent, deviceID := enroll("rmm-host")
	other, otherDevice := enroll("other-host")

	older := run(agent, "system.info", `{"hostname":"rmm-host","uptimeSeconds":1}`)
	newer := run(agent, "system.info", `{"hostname":"rmm-host","uptimeSeconds":2}`)
	processes := run(agent, "process.list", `{"items":[{"pid":7,"name":"nginx"}],"returned":1}`)
	pending := run(agent, "process.list", "") // claimed, no result yet
	foreign := run(other, "system.info", `{"hostname":"other-host","uptimeSeconds":3}`)

	snapshotsURL := server.URL + "/api/v1/devices/" + deviceID + "/remote/snapshots"
	if response := doJSON(t, viewer, http.MethodGet, snapshotsURL, "", nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer snapshots = %d", response.StatusCode)
	} else {
		_ = response.Body.Close()
	}
	var snapshots struct {
		Items []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Result struct {
				Output json.RawMessage `json:"output"`
			} `json:"result"`
			RequestedByEmail string `json:"requestedByEmail"`
		} `json:"items"`
	}
	decodeResponse(t, doJSON(t, technician, http.MethodGet, snapshotsURL, "", nil), &snapshots)
	byType := map[string]string{}
	for _, item := range snapshots.Items {
		byType[item.Type] = item.ID
		if len(item.Result.Output) == 0 || item.RequestedByEmail != "admin@example.invalid" {
			t.Fatalf("snapshot lacks output or provenance: %+v", item)
		}
	}
	if len(byType) != 2 || byType["system.info"] != newer || byType["process.list"] != processes {
		t.Fatalf("snapshots = %+v, want newest system.info %s and process.list %s (not %s, %s, %s)", byType, newer, processes, older, pending, foreign)
	}

	auditURL := server.URL + "/api/v1/devices/" + deviceID + "/audit"
	for client, name := range map[*http.Client]string{technician: "technician", viewer: "viewer"} {
		if response := doJSON(t, client, http.MethodGet, auditURL, "", nil); response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s device audit = %d", name, response.StatusCode)
		} else {
			_ = response.Body.Close()
		}
	}
	trail := readBody(doJSON(t, admin, http.MethodGet, auditURL, "", nil))
	for _, expected := range []string{newer, processes, pending, `"targetId":"` + agent.agentID + `"`, `"action":"remote.command.completed"`} {
		if !strings.Contains(trail, expected) {
			t.Fatalf("device audit lacks %s: %s", expected, trail)
		}
	}
	if strings.Contains(trail, foreign) || strings.Contains(trail, other.agentID) || strings.Contains(trail, otherDevice) {
		t.Fatalf("device audit leaks another device's records: %s", trail)
	}
	if response := doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/devices/not-a-uuid/audit", "", nil); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid device audit = %d", response.StatusCode)
	} else {
		_ = response.Body.Close()
	}
}
