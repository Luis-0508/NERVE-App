//go:build integration

package httpapi_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/authz"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestPrivilegedApprovalOverHTTP(t *testing.T) {
	unregister, err := remote.RegisterPolicy(remote.Policy{Type: agentproto.CapabilitySystemReboot, Risk: agentproto.RiskPrivileged,
		Permission: authz.RemoteExecutePrivileged, DefaultTimeout: 30 * time.Second, MaxTimeout: time.Minute, MaxOutputBytes: 4096, RequiresApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	pool := testsupport.MigratedPool(t, "http_approval")
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour, CoreSigningKey: testsupport.CoreSigningKeyHex},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")

	var created struct {
		Token string `json:"token"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{}`, nil), &created)
	agent := enrollTestAgent(t, server.URL, created.Token, "reboot-host")
	heartbeat := func(transport string) {
		t.Helper()
		body, _ := json.Marshal(agentproto.HeartbeatRequest{Capabilities: []string{agentproto.CapabilitySystemReboot}, Transport: transport,
			Host: agentproto.HostFacts{Hostname: "reboot-host", OS: "linux", Architecture: "amd64", AgentVersion: "test"}})
		if status, response := send(t, http.DefaultClient, agent.request("POST", "/agent/heartbeat", body, agent.credentialID, nil)); status != http.StatusOK {
			t.Fatalf("heartbeat = %d: %s", status, response)
		}
	}
	commandsURL := server.URL + "/api/v1/agents/" + agent.agentID + "/commands"
	expect := func(client *http.Client, url, body string, status int, code string) string {
		t.Helper()
		response := doJSON(t, client, http.MethodPost, url, body, nil)
		text := readBody(response)
		if response.StatusCode != status || (code != "" && !strings.Contains(text, `"code":"`+code+`"`)) {
			t.Fatalf("POST %s = %d %s, want %d %s", url, response.StatusCode, text, status, code)
		}
		return text
	}
	reboot := `{"type":"system.reboot"}`

	expect(technician, commandsURL, reboot, http.StatusForbidden, "permission_required")
	heartbeat(agentproto.TransportHTTP)
	expect(admin, commandsURL, reboot, http.StatusConflict, "transport_not_https")
	heartbeat(agentproto.TransportHTTPS)
	expect(admin, commandsURL, reboot, http.StatusConflict, "device_not_trusted")

	ctx := context.Background()
	deviceID := testsupport.CreateDevice(t, pool, "reboot-host", "", "")
	if _, err := pool.Exec(ctx, `UPDATE agents SET device_id = $2::uuid, association_status = 'confirmed', associated_at = now(), associated_by = $3::uuid
		WHERE id = $1::uuid`, agent.agentID, deviceID, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE devices SET trust_state = 'trusted', trust_changed_by = $2::uuid, trust_changed_at = clock_timestamp()
		WHERE entity_id = $1::uuid`, deviceID, adminID); err != nil {
		t.Fatal(err)
	}

	var command remote.Command
	if err := json.Unmarshal([]byte(expect(admin, commandsURL, reboot, http.StatusCreated, "")), &command); err != nil {
		t.Fatal(err)
	}
	if command.Status != "awaiting_approval" || command.Target == nil || command.Target.DeviceID != deviceID || command.Target.TrustState != "trusted" {
		t.Fatalf("created command: %+v target=%+v", command, command.Target)
	}
	if status, _ := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{"waitSeconds":0}`), agent.credentialID, nil)); status != http.StatusNoContent {
		t.Fatalf("unapproved claim = %d", status)
	}
	approveURL := server.URL + "/api/v1/remote/commands/" + command.ID + "/approve"
	expect(technician, approveURL, `{}`, http.StatusForbidden, "permission_required")
	expect(admin, approveURL, `{}`, http.StatusOK, "")
	expect(admin, approveURL, `{}`, http.StatusConflict, "command_not_awaiting_approval")

	status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/commands/claim", []byte(`{"waitSeconds":0}`), agent.credentialID, nil))
	var claimed agentproto.Command
	if status != http.StatusOK || json.Unmarshal([]byte(body), &claimed) != nil || claimed.ID != command.ID {
		t.Fatalf("approved claim = %d: %s", status, body)
	}
	if err := agentproto.VerifyCommandApproval(testsupport.CoreKey().Public().(ed25519.PublicKey), claimed, time.Now()); err != nil {
		t.Fatalf("approval envelope: %v", err)
	}

	auditBody := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/audit?targetType=remote_command&targetId="+command.ID, "", nil))
	for _, action := range []string{"remote.command.created", "remote.command.approved", "remote.command.claimed"} {
		if !strings.Contains(auditBody, `"action":"`+action+`"`) {
			t.Fatalf("audit trail lacks %s: %s", action, auditBody)
		}
	}
}
