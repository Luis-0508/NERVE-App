package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

func TestIdentityRoundTripIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	_, private, _ := ed25519.GenerateKey(nil)
	original := &identity{AgentID: "a1", CredentialID: "c1", Fingerprint: "f", APIURL: "http://core/api/v1", key: private, coreKey: testCorePublic}
	if err := saveIdentity(dir, original); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgentID != "a1" || !loaded.key.Equal(private) {
		t.Fatal("identity did not round-trip")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, identityFileName))
		dirInfo, _ := os.Stat(dir)
		if info.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("identity permissions %v / %v", info.Mode().Perm(), dirInfo.Mode().Perm())
		}
		if err := os.Chmod(filepath.Join(dir, identityFileName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadIdentity(dir); err == nil {
			t.Fatal("world-readable identity file was accepted")
		}
	}
}

// testCorePublic and testCoreKey stand in for NERVE Core's signing key.
var testCorePublic, testCoreKey, _ = ed25519.GenerateKey(nil)

// fakeCore verifies agent signatures and signs its responses exactly as Core does.
func fakeCore(t *testing.T, public *ed25519.PublicKey, handler func(route string, body []byte) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers, err := agentproto.ParseHeaders(r.Header)
		route := agentproto.Route(r.URL.Path, r.URL.RawQuery)
		key := *public
		if route == "/agent/enroll" {
			var request agentproto.EnrollRequest
			_ = json.Unmarshal(body, &request)
			key, _ = agentproto.DecodePublicKey(request.PublicKey)
			*public = key
		}
		status, encoded := http.StatusUnauthorized, []byte(`{"error":{"code":"agent_authentication_failed","message":"bad signature"}}`)
		if err == nil && headers.Verify(key, r.Method, route, body) == nil && headers.CheckTimestamp(time.Now()) == nil {
			var response any
			status, response = handler(route, body)
			encoded, _ = json.Marshal(response)
			if status == http.StatusNoContent {
				encoded = nil
			}
		}
		w.Header().Set("Content-Type", "application/json")
		agentproto.SignCoreResponse(w.Header(), testCoreKey, r.Header.Get(agentproto.HeaderSignature), status, encoded)
		w.WriteHeader(status)
		_, _ = w.Write(encoded)
	}))
}

func TestEnrollSignsWithNewKeyAndChecksFingerprint(t *testing.T) {
	var public ed25519.PublicKey
	token := agentproto.FormatEnrollmentToken("3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", agentproto.CoreFingerprint(testCorePublic))
	wrongFingerprint := false
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		var request agentproto.EnrollRequest
		_ = json.Unmarshal(body, &request)
		if request.Token != token || request.Host.Hostname == "" {
			return 400, nil
		}
		fingerprint := agentproto.Fingerprint(public)
		if wrongFingerprint {
			fingerprint = "00"
		}
		return 201, agentproto.EnrollResponse{AgentID: "agent-1", CredentialID: "cred-1", Fingerprint: fingerprint}
	})
	defer server.Close()

	id, err := enroll(context.Background(), server.URL+"/api/v1", token)
	if err != nil {
		t.Fatal(err)
	}
	if id.AgentID != "agent-1" || !id.key.Public().(ed25519.PublicKey).Equal(public) {
		t.Fatal("enrollment did not keep the generated key")
	}
	wrongFingerprint = true
	if _, err := enroll(context.Background(), server.URL+"/api/v1", token); err == nil {
		t.Fatal("enrollment accepted a mismatched fingerprint")
	}
	if _, err := enroll(context.Background(), server.URL+"/api/v1", "not-a-token"); err == nil {
		t.Fatal("malformed token was sent to Core")
	}
}

func TestHeartbeatIsSignedAndClassifiesRevocation(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	var heartbeats atomic.Int32
	revoked := atomic.Bool{}
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		if revoked.Load() {
			return 401, map[string]any{"error": map[string]string{"code": agentproto.CodeCredentialRevoked}}
		}
		var request agentproto.HeartbeatRequest
		if route != "/agent/heartbeat" || json.Unmarshal(body, &request) != nil || request.Host.OS == "" {
			return 400, nil
		}
		heartbeats.Add(1)
		return 200, agentproto.HeartbeatResponse{ServerTime: time.Now().Unix()}
	})
	defer server.Close()
	m := &manager{client: newManagementClient(server.URL+"/api/v1", &identity{CredentialID: "cred-1", key: private, coreKey: testCorePublic}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), capabilities: supportedCapabilities, facts: collectHostFacts}
	if err := m.heartbeat(context.Background()); err != nil || heartbeats.Load() != 1 {
		t.Fatalf("heartbeat failed: %v", err)
	}
	revoked.Store(true)
	if err := m.heartbeat(context.Background()); !requiresReenrollment(err) {
		t.Fatalf("revocation not classified: %v", err)
	}
	// A signature from a different key is rejected by Core.
	_, other, _ := ed25519.GenerateKey(nil)
	revoked.Store(false)
	m.client.key = other
	if err := m.heartbeat(context.Background()); !requiresReenrollment(err) {
		t.Fatalf("foreign key accepted: %v", err)
	}
}

func TestBackoffIsBounded(t *testing.T) {
	for failures := 1; failures < 20; failures++ {
		delay := backoff(failures)
		if delay < 1500*time.Millisecond || delay > 72*time.Second {
			t.Fatalf("backoff(%d) = %s", failures, delay)
		}
	}
}

func TestHostFactsAreValid(t *testing.T) {
	facts := collectHostFacts()
	if err := facts.Validate(); err != nil {
		t.Fatalf("collected host facts are invalid: %v", err)
	}
	if facts.MachineFingerprint != "" && len(facts.MachineFingerprint) != 64 {
		t.Fatal("machine ID is not digested")
	}
}

func TestHeartbeatClearsOnlyTheReportedError(t *testing.T) {
	m := &manager{}
	m.setError("first")
	m.setError("newer")
	m.clearError("first")
	if m.currentError() != "newer" {
		t.Fatal("a newer error was cleared")
	}
	m.clearError("newer")
	if m.currentError() != "" {
		t.Fatal("the reported error was not cleared")
	}
}

func TestEnrollPinsCoreKeyFromToken(t *testing.T) {
	var public ed25519.PublicKey
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		return 201, agentproto.EnrollResponse{AgentID: "agent-1", CredentialID: "cred-1", Fingerprint: agentproto.Fingerprint(public)}
	})
	defer server.Close()
	id := "3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e"
	secret := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	enrolled, err := enroll(context.Background(), server.URL+"/api/v1", agentproto.FormatEnrollmentToken(id, secret, agentproto.CoreFingerprint(testCorePublic)))
	if err != nil || !enrolled.coreKey.Equal(testCorePublic) {
		t.Fatalf("Core key not pinned: %v", err)
	}
	// A token that pins a different Core key refuses this server (for example a MITM).
	otherCore, _, _ := ed25519.GenerateKey(nil)
	if _, err := enroll(context.Background(), server.URL+"/api/v1", agentproto.FormatEnrollmentToken(id, secret, agentproto.CoreFingerprint(otherCore))); !errors.Is(err, agentproto.ErrUnverifiedResponse) {
		t.Fatalf("enrollment trusted an unpinned Core key: %v", err)
	}
}

func TestForgedCoreResponsesAreIgnored(t *testing.T) {
	agentPublic, agentKey, _ := ed25519.GenerateKey(nil)
	// An attacker answers with a correct-looking but self-signed "revoked" response.
	_, attackerKey, _ := ed25519.GenerateKey(nil)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := []byte(`{"error":{"code":"agent_credential_revoked","message":"forged"}}`)
		agentproto.SignCoreResponse(w.Header(), attackerKey, r.Header.Get(agentproto.HeaderSignature), 401, body)
		w.WriteHeader(401)
		_, _ = w.Write(body)
	}))
	defer attacker.Close()
	_ = agentPublic
	m := &manager{client: newManagementClient(attacker.URL+"/api/v1", &identity{CredentialID: "cred", key: agentKey, coreKey: testCorePublic}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), capabilities: supportedCapabilities, facts: collectHostFacts}
	err := m.heartbeat(context.Background())
	if !errors.Is(err, agentproto.ErrUnverifiedResponse) || requiresReenrollment(err) {
		t.Fatalf("forged revocation was trusted: %v", err)
	}
	// A forged command is never executed.
	executed := false
	m.collectors = map[string]collector{"system.info": func(context.Context, map[string]any) (map[string]any, error) { executed = true; return nil, nil }}
	var command agentproto.Command
	if _, err := m.client.post(context.Background(), "/agent/commands/claim", agentproto.ClaimRequest{}, &command); err == nil || command.ID != "" || executed {
		t.Fatalf("forged claim response accepted: %v %+v", err, command)
	}
}
