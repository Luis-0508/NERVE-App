//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// sendDiscovery sends a request and, for a signed one, requires a Core
// signature bound to it, exactly as the agent's discovery worker does.
func sendDiscovery(t *testing.T, request *http.Request) (int, string) {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if signature := request.Header.Get(agentproto.HeaderSignature); signature != "" {
		if err := agentproto.VerifyCoreResponse(testsupport.CoreKey().Public().(ed25519.PublicKey), response.Header, signature, response.StatusCode, body); err != nil {
			t.Fatalf("%s response (HTTP %d) is not signed by Core: %v", request.URL.Path, response.StatusCode, err)
		}
	}
	return response.StatusCode, string(body)
}

func (a *testAgent) discovery(route, body string, extra map[string]string) (int, string) {
	a.t.Helper()
	return sendDiscovery(a.t, a.request(http.MethodPost, "/discovery/agent"+route, []byte(body), a.credentialID, extra))
}

func (a *testAgent) managementHeartbeat(capabilities ...string) {
	a.t.Helper()
	body, _ := json.Marshal(agentproto.HeartbeatRequest{Host: agentproto.HostFacts{Hostname: "scanner", OS: "linux", Architecture: "amd64", AgentVersion: "test"},
		Capabilities: capabilities})
	if status, response := send(a.t, http.DefaultClient, a.request(http.MethodPost, "/agent/heartbeat", body, a.credentialID, nil)); status != http.StatusOK {
		a.t.Fatalf("management heartbeat = %d: %s", status, response)
	}
}

func TestSignedDiscoveryWorker(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_discovery_identity")
	testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	const legacyToken = "legacy-discovery-token"
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour,
		CoreSigningKey: testsupport.CoreSigningKeyHex, DiscoveryAgentToken: legacyToken, DiscoveryLegacyToken: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")

	var enrollment struct {
		Token string `json:"token"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{"displayName":"Scanner","expiresInMinutes":30}`, nil), &enrollment)
	agent := enrollTestAgent(t, server.URL, enrollment.Token, "scanner")
	heartbeat := `{"id":"spoofed","hostname":"scanner","platform":"linux/amd64","version":"test","capabilities":["icmp"]}`

	// Unsigned and capability-less requests are refused.
	if status, body := sendDiscovery(t, mustRequest(t, server.URL+"/api/v1/discovery/agent/jobs/claim", `{}`, nil)); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated claim = %d: %s", status, body)
	}
	if status, body := agent.discovery("/heartbeat", heartbeat, nil); status != http.StatusForbidden || !strings.Contains(body, "capability_not_advertised") {
		t.Fatalf("heartbeat without discovery.scan = %d: %s", status, body)
	}

	agent.managementHeartbeat(agentproto.CapabilitySystemInfo, agentproto.CapabilityDiscoveryScan)
	if status, body := agent.discovery("/heartbeat", heartbeat, nil); status != http.StatusNoContent {
		t.Fatalf("signed heartbeat = %d: %s", status, body)
	}
	var state discovery.State
	decodeResponse(t, doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/discovery", "", nil), &state)
	if state.Agent == nil || state.Agent.ID != agent.agentID || state.Agent.AgentID != agent.agentID || state.Agent.LegacyToken {
		t.Fatalf("discovery state after signed heartbeat: %+v", state.Agent)
	}

	var target struct {
		ID string `json:"id"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/discovery/target", `{"name":"Lab","cidr":"192.0.2.0/24"}`, nil), &target)
	queued := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/discovery/scans", fmt.Sprintf(`{"targetId":%q}`, target.ID), nil)
	if queued.StatusCode != http.StatusAccepted {
		t.Fatalf("queue scan = %d: %s", queued.StatusCode, readBody(queued))
	}
	_ = queued.Body.Close()

	// The worker identity comes from the signature; a claimed header is ignored.
	claim := agent.request(http.MethodPost, "/discovery/agent/jobs/claim", []byte(`{}`), agent.credentialID, map[string]string{"X-NERVE-Agent-ID": "someone-else"})
	replay := claim.Clone(context.Background())
	replay.Body = io.NopCloser(bytes.NewReader([]byte(`{}`)))
	status, body := sendDiscovery(t, claim)
	if status != http.StatusOK {
		t.Fatalf("signed claim = %d: %s", status, body)
	}
	var job discovery.Job
	if err := json.Unmarshal([]byte(body), &job); err != nil || job.CIDR != "192.0.2.0/24" {
		t.Fatalf("claimed job %q: %v", body, err)
	}
	var scanAgent string
	if err := pool.QueryRow(context.Background(), `SELECT agent_id FROM discovery_scans WHERE id = $1::uuid`, job.ID).Scan(&scanAgent); err != nil || scanAgent != agent.agentID {
		t.Fatalf("scan attributed to %q (%v), want %s", scanAgent, err, agent.agentID)
	}
	if status, body := sendDiscovery(t, replay); status != http.StatusUnauthorized || !strings.Contains(body, agentproto.CodeReplay) {
		t.Fatalf("replayed claim = %d: %s", status, body)
	}

	report := `{"observations":[{"address":"192.0.2.10","reachable":true,"method":"icmp"}],"errors":[]}`
	if status, body := agent.discovery("/jobs/"+job.ID+"/report", report, nil); status != http.StatusNoContent {
		t.Fatalf("signed report = %d: %s", status, body)
	}
	// A legacy token holder cannot report on the enrolled agent's scan.
	legacyHeaders := map[string]string{"Authorization": "Bearer " + legacyToken, "X-NERVE-Agent-ID": "scanner-linux"}
	if status, _ := sendDiscovery(t, mustRequest(t, server.URL+"/api/v1/discovery/agent/jobs/"+job.ID+"/report", report, legacyHeaders)); status != http.StatusUnprocessableEntity {
		t.Fatalf("legacy report on a signed scan = %d", status)
	}
	if status, body := agent.discovery("/jobs/"+job.ID+"/complete", `{"addressesTotal":254,"status":"succeeded"}`, nil); status != http.StatusNoContent {
		t.Fatalf("signed completion = %d: %s", status, body)
	}
	var audited int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE actor_agent_id = $1::uuid
		AND action IN ('discovery.scan.claimed', 'discovery.scan.completed') AND target_id = $2`, agent.agentID, job.ID).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audited discovery actions = %d (%v), want 2", audited, err)
	}

	// The legacy path still works next to the signed one, but cannot take
	// over the enrolled agent's worker row.
	if status, body := sendDiscovery(t, mustRequest(t, server.URL+"/api/v1/discovery/agent/heartbeat", heartbeat, legacyHeaders)); status != http.StatusNoContent {
		t.Fatalf("legacy heartbeat = %d: %s", status, body)
	}
	repository := discovery.NewRepository(pool)
	if err := repository.Heartbeat(context.Background(), discovery.LegacyWorker(agent.agentID), discovery.AgentHeartbeat{Hostname: "x", Platform: "x", Version: "x"}); !errors.Is(err, discovery.ErrWorkerConflict) {
		t.Fatalf("legacy heartbeat on an enrolled row: %v", err)
	}

	// Dropping the capability stops discovery; revocation ends it entirely.
	agent.managementHeartbeat(agentproto.CapabilitySystemInfo)
	if status, body := agent.discovery("/jobs/claim", `{}`, nil); status != http.StatusForbidden || !strings.Contains(body, "capability_not_advertised") {
		t.Fatalf("claim after dropping discovery.scan = %d: %s", status, body)
	}
	revoked := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agents/"+agent.agentID+"/revoke", `{}`, nil)
	if revoked.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d: %s", revoked.StatusCode, readBody(revoked))
	}
	_ = revoked.Body.Close()
	if status, body := agent.discovery("/jobs/claim", `{}`, nil); status != http.StatusUnauthorized || !strings.Contains(body, agentproto.CodeCredentialRevoked) {
		t.Fatalf("claim after revocation = %d: %s", status, body)
	}
}

func TestPendingAgentCannotRunDiscovery(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_discovery_pending")
	testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour,
		CoreSigningKey: testsupport.CoreSigningKeyHex}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")

	var created struct {
		Key string `json:"key"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-deployment-keys", `{"name":"Office","approval":"manual","maxUses":1,"expiresInDays":1}`, nil), &created)
	agent := enrollTestAgent(t, server.URL, created.Key, "scanner")
	agent.managementHeartbeat(agentproto.CapabilityDiscoveryScan)
	if status, body := agent.discovery("/jobs/claim", `{}`, nil); status != http.StatusForbidden || !strings.Contains(body, "agent_not_active") {
		t.Fatalf("pending agent claim = %d: %s", status, body)
	}

	// With the legacy path off, the shared token opens nothing.
	if status, body := sendDiscovery(t, mustRequest(t, server.URL+"/api/v1/discovery/agent/jobs/claim", `{}`,
		map[string]string{"Authorization": "Bearer anything", "X-NERVE-Agent-ID": "host-linux"})); status != http.StatusUnauthorized || !strings.Contains(body, "legacy_discovery_disabled") {
		t.Fatalf("legacy claim with the path disabled = %d: %s", status, body)
	}
}

func mustRequest(t *testing.T, url, body string, headers map[string]string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request
}
