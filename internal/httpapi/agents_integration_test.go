//go:build integration

package httpapi_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// testAgent is a minimal signing client used to exercise the agent API.
type testAgent struct {
	t            *testing.T
	baseURL      string
	key          ed25519.PrivateKey
	credentialID string
	agentID      string
}

func (a *testAgent) request(method, route string, body []byte, credential string, extra map[string]string) *http.Request {
	a.t.Helper()
	request, err := http.NewRequest(method, a.baseURL+"/api/v1"+route, bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if err := agentproto.Sign(request, credential, a.key, body, time.Now()); err != nil {
		a.t.Fatal(err)
	}
	for key, value := range extra {
		request.Header.Set(key, value)
	}
	return request
}

func send(t *testing.T, client *http.Client, request *http.Request) (int, string) {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func enrollTestAgent(t *testing.T, baseURL, token, hostname string) *testAgent {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(nil)
	agent := &testAgent{t: t, baseURL: baseURL, key: private}
	body, _ := json.Marshal(agentproto.EnrollRequest{Token: token, PublicKey: agentproto.EncodePublicKey(private.Public().(ed25519.PublicKey)),
		Host: agentproto.HostFacts{Hostname: hostname, OS: "linux", Architecture: "amd64", AgentVersion: "test"}})
	status, response := send(t, http.DefaultClient, agent.request("POST", "/agent/enroll", body, agentproto.CredentialSelfSigned, nil))
	if status != http.StatusCreated {
		t.Fatalf("enroll status = %d: %s", status, response)
	}
	var result agentproto.EnrollResponse
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		t.Fatal(err)
	}
	agent.credentialID, agent.agentID = result.CredentialID, result.AgentID
	return agent
}

func login(t *testing.T, baseURL, email, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	response := doJSON(t, client, http.MethodPost, baseURL+"/api/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login %s = %d: %s", email, response.StatusCode, readBody(response))
	}
	_ = response.Body.Close()
	return client
}

func TestAgentEnrollmentAPIBoundaries(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_agents")
	testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	testsupport.CreateUser(t, pool, "viewer@example.invalid", "integration-password", "read_only")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()

	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")
	viewer := login(t, server.URL, "viewer@example.invalid", "integration-password")

	// Session exposes server-computed permissions for the UI.
	sessionResponse := doJSON(t, technician, http.MethodGet, server.URL+"/api/v1/auth/session", "", nil)
	sessionBody := readBody(sessionResponse)
	if !strings.Contains(sessionBody, `"remote.execute.read_only"`) || strings.Contains(sessionBody, `"agents.manage"`) {
		t.Fatalf("unexpected technician permissions: %s", sessionBody)
	}

	// Only agents.manage may create enrollments; denials are audited.
	for _, client := range []*http.Client{technician, viewer} {
		response := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{"displayName":"x"}`, nil)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("non-manager enrollment status = %d", response.StatusCode)
		}
		_ = response.Body.Close()
	}
	anonymous := doJSON(t, http.DefaultClient, http.MethodGet, server.URL+"/api/v1/agents", "", nil)
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous agent list status = %d", anonymous.StatusCode)
	}
	_ = anonymous.Body.Close()

	created := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{"displayName":"Lab","expiresInMinutes":30}`, nil)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create enrollment status = %d: %s", created.StatusCode, readBody(created))
	}
	var enrollment struct {
		Token      string `json:"token"`
		Enrollment struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"enrollment"`
	}
	decodeResponse(t, created, &enrollment)
	if enrollment.Enrollment.State != "pending" || !strings.HasPrefix(enrollment.Token, agentproto.EnrollmentTokenPrefix) {
		t.Fatalf("unexpected enrollment response: %+v", enrollment)
	}
	listed := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/agent-enrollments", "", nil))
	_, secret, _ := agentproto.ParseEnrollmentToken(enrollment.Token)
	if strings.Contains(listed, strings.Split(enrollment.Token, ".")[2]) || len(secret) != 32 {
		t.Fatal("enrollment listing exposes the token secret")
	}

	// An enrollment request signed with a key other than the submitted one is rejected.
	_, mismatched, _ := ed25519.GenerateKey(nil)
	otherPublic, _, _ := ed25519.GenerateKey(nil)
	mismatch := &testAgent{t: t, baseURL: server.URL, key: mismatched}
	mismatchBody, _ := json.Marshal(agentproto.EnrollRequest{Token: enrollment.Token, PublicKey: agentproto.EncodePublicKey(otherPublic),
		Host: agentproto.HostFacts{Hostname: "x", OS: "linux"}})
	if status, body := send(t, http.DefaultClient, mismatch.request("POST", "/agent/enroll", mismatchBody, agentproto.CredentialSelfSigned, nil)); status != http.StatusUnauthorized {
		t.Fatalf("mismatched proof of possession status = %d: %s", status, body)
	}

	agent := enrollTestAgent(t, server.URL, enrollment.Token, "lab-host")

	identity := agent.request("POST", "/agent/identity", []byte(`{}`), agent.credentialID, nil)
	if status, body := send(t, http.DefaultClient, identity); status != http.StatusOK || !strings.Contains(body, agent.agentID) {
		t.Fatalf("identity status = %d: %s", status, body)
	}
	// Replaying a captured signed request fails.
	replay := agent.request("POST", "/agent/identity", []byte(`{}`), agent.credentialID, nil)
	if status, _ := send(t, http.DefaultClient, replay.Clone(replay.Context())); status != http.StatusOK {
		t.Fatal("first use of signed request failed")
	}
	replay.Body = io.NopCloser(bytes.NewReader([]byte(`{}`)))
	if status, body := send(t, http.DefaultClient, replay); status != http.StatusUnauthorized || !strings.Contains(body, agentproto.CodeReplay) {
		t.Fatalf("replay status = %d: %s", status, body)
	}
	// Browser-originated requests never reach agent endpoints.
	if status, _ := send(t, http.DefaultClient, agent.request("POST", "/agent/identity", []byte(`{}`), agent.credentialID, map[string]string{"Origin": "http://localhost:3000"})); status != http.StatusForbidden {
		t.Fatalf("browser request to agent API status = %d", status)
	}
	// A browser session cannot call agent endpoints.
	sessionOnly := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent/identity", `{}`, nil)
	if sessionOnly.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session on agent API status = %d", sessionOnly.StatusCode)
	}
	_ = sessionOnly.Body.Close()
	// Agent credentials cannot call operator endpoints.
	if status, _ := send(t, http.DefaultClient, agent.request("GET", "/agents", nil, agent.credentialID, nil)); status != http.StatusUnauthorized {
		t.Fatalf("agent credential on operator API status = %d", status)
	}

	// Viewers can list agents but cannot revoke.
	if list := readBody(doJSON(t, viewer, http.MethodGet, server.URL+"/api/v1/agents", "", nil)); !strings.Contains(list, agent.agentID) || !strings.Contains(list, `"online":true`) {
		t.Fatalf("viewer agent list: %s", list)
	}
	denied := doJSON(t, viewer, http.MethodPost, server.URL+"/api/v1/agents/"+agent.agentID+"/revoke", `{}`, nil)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer revoke status = %d", denied.StatusCode)
	}
	_ = denied.Body.Close()

	revoked := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agents/"+agent.agentID+"/revoke", `{}`, nil)
	if revoked.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d: %s", revoked.StatusCode, readBody(revoked))
	}
	_ = revoked.Body.Close()
	if status, body := send(t, http.DefaultClient, agent.request("POST", "/agent/identity", []byte(`{}`), agent.credentialID, nil)); status != http.StatusUnauthorized || !strings.Contains(body, agentproto.CodeCredentialRevoked) {
		t.Fatalf("revoked agent status = %d: %s", status, body)
	}

	// Audit trail: creation, completion, revocation and denials, all without secrets.
	auditBody := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/audit?limit=100", "", nil))
	for _, action := range []string{"agent.enrollment.created", "agent.enrollment.completed", "agent.credential.revoked", "authz.denied"} {
		if !strings.Contains(auditBody, `"action":"`+action+`"`) {
			t.Fatalf("audit log lacks %s: %s", action, auditBody)
		}
	}
	if strings.Contains(auditBody, strings.Split(enrollment.Token, ".")[2]) {
		t.Fatal("audit log contains the enrollment secret")
	}
	technicianAudit := doJSON(t, technician, http.MethodGet, server.URL+"/api/v1/audit", "", nil)
	if technicianAudit.StatusCode != http.StatusForbidden {
		t.Fatalf("technician audit status = %d", technicianAudit.StatusCode)
	}
	_ = technicianAudit.Body.Close()

	// Discovery administration now requires discovery.manage and is audited.
	techTarget := doJSON(t, technician, http.MethodPost, server.URL+"/api/v1/discovery/target", `{"cidr":"192.168.50.0/24"}`, nil)
	if techTarget.StatusCode != http.StatusForbidden {
		t.Fatalf("technician discovery target status = %d", techTarget.StatusCode)
	}
	_ = techTarget.Body.Close()
	target := doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/discovery/target", `{"cidr":"192.168.50.0/24"}`, nil)
	if target.StatusCode != http.StatusOK {
		t.Fatalf("admin discovery target status = %d", target.StatusCode)
	}
	_ = target.Body.Close()
	if body := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/audit?action=discovery.", "", nil)); !strings.Contains(body, `"discovery.target.saved"`) {
		t.Fatalf("discovery change not audited: %s", body)
	}
}
