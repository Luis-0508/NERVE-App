package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// rotationCore models Core's credential table: it authenticates any known,
// unrevoked credential and implements the rotation endpoint. commit=false
// simulates a crash before Core stored the new key; dropResponse simulates a
// crash after Core committed but before the agent saw the answer.
type rotationCore struct {
	mu           sync.Mutex
	agentID      string
	keys         map[string]ed25519.PublicKey
	revoked      map[string]bool
	commit       bool
	dropResponse bool
	// legacy answers the rotation route like a Core without it: an unsigned 404.
	legacy bool
	server       *httptest.Server
}

func newRotationCore(t *testing.T, id *identity) *rotationCore {
	t.Helper()
	core := &rotationCore{agentID: id.AgentID, commit: true, revoked: map[string]bool{},
		keys: map[string]ed25519.PublicKey{id.CredentialID: id.key.Public().(ed25519.PublicKey)}}
	core.server = httptest.NewServer(http.HandlerFunc(core.serve))
	t.Cleanup(core.server.Close)
	return core
}

func (c *rotationCore) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	route := agentproto.Route(r.URL.Path, r.URL.RawQuery)
	if c.legacy && route == "/agent/credential/rotate" {
		http.NotFound(w, r)
		return
	}
	status, response := http.StatusUnauthorized, any(map[string]any{"error": map[string]string{"code": agentproto.CodeAuthenticationFailed, "message": "unknown"}})
	headers, err := agentproto.ParseHeaders(r.Header)
	key, known := c.keys[headers.CredentialID]
	switch {
	case err != nil || !known || headers.Verify(key, r.Method, route, body) != nil:
	case c.revoked[headers.CredentialID]:
		response = map[string]any{"error": map[string]string{"code": agentproto.CodeCredentialRevoked, "message": "revoked"}}
	case route == "/agent/identity":
		status, response = http.StatusOK, agentproto.IdentityResponse{AgentID: c.agentID, CredentialID: headers.CredentialID, Status: "active"}
	case route == "/agent/credential/rotate":
		var request agentproto.RotateCredentialRequest
		_ = json.Unmarshal(body, &request)
		public, err := agentproto.VerifyRotation(request, c.agentID, headers.CredentialID, time.Now())
		if err != nil {
			status, response = http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "invalid_rotation_proof", "message": err.Error()}}
			break
		}
		if !c.commit {
			hijackAndClose(w)
			return
		}
		c.keys[request.CredentialID] = public
		c.revoked[headers.CredentialID] = true
		if c.dropResponse {
			hijackAndClose(w)
			return
		}
		status, response = http.StatusOK, agentproto.RotateCredentialResponse{CredentialID: request.CredentialID,
			Fingerprint: agentproto.Fingerprint(public), CreatedAt: time.Now().UTC()}
	}
	encoded, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json")
	agentproto.SignCoreResponse(w.Header(), testCoreKey, r.Header.Get(agentproto.HeaderSignature), status, encoded)
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// hijackAndClose drops the connection without an answer, like a crash.
func hijackAndClose(w http.ResponseWriter) {
	if connection, _, err := w.(http.Hijacker).Hijack(); err == nil {
		_ = connection.Close()
	}
}

func enrolledIdentity(t *testing.T, dir string) *identity {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(nil)
	id := &identity{AgentID: "6a1b2c3d-4e5f-4a6b-8c7d-8e9f0a1b2c3d", CredentialID: "7a1b2c3d-4e5f-4a6b-8c7d-8e9f0a1b2c3d",
		Fingerprint: agentproto.Fingerprint(key.Public().(ed25519.PublicKey)), EnrolledAt: time.Now().Add(-48 * time.Hour).UTC(),
		key: key, coreKey: testCorePublic}
	if err := saveIdentity(dir, id); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func pendingExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, pendingRotationFileName))
	return err == nil
}

func TestRotateCredentialReplacesIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)

	next, err := rotateCredential(context.Background(), dir, core.server.URL+"/api/v1", id, nil, time.Now())
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if next.CredentialID == id.CredentialID || next.key.Equal(id.key) || next.AgentID != id.AgentID {
		t.Fatalf("identity not replaced: %+v", next)
	}
	if pendingExists(dir) {
		t.Fatal("pending key was not removed after a confirmed rotation")
	}
	saved, err := loadIdentity(dir)
	if err != nil || saved.CredentialID != next.CredentialID || !saved.key.Equal(next.key) {
		t.Fatalf("identity.json not updated: %+v, %v", saved, err)
	}
	if age := saved.credentialAge(time.Now()); age > time.Minute {
		t.Fatalf("credential age after rotation = %v", age)
	}
	if !core.revoked[id.CredentialID] {
		t.Fatal("old credential still active in Core")
	}
}

func TestRotationCrashAfterCoreCommitPromotesPendingKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)
	core.dropResponse = true
	apiURL := core.server.URL + "/api/v1"

	if _, err := rotateCredential(context.Background(), dir, apiURL, id, nil, time.Now()); err == nil {
		t.Fatal("rotation without a response reported success")
	}
	if !pendingExists(dir) {
		t.Fatal("pending key was discarded although Core may have committed it")
	}
	if current, _ := loadIdentity(dir); current.CredentialID != id.CredentialID {
		t.Fatal("identity.json changed before Core confirmed the rotation")
	}
	// A second rotation must not start while the first is unresolved.
	if _, err := rotateCredential(context.Background(), dir, apiURL, id, nil, time.Now()); err == nil {
		t.Fatal("started a new rotation over an unresolved one")
	}

	next, err := resolvePendingRotation(context.Background(), dir, apiURL, id, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if next.CredentialID == id.CredentialID || pendingExists(dir) {
		t.Fatal("pending key was not promoted although Core committed it")
	}
	saved, _ := loadIdentity(dir)
	if saved.CredentialID != next.CredentialID {
		t.Fatal("promoted key was not saved")
	}
}

func TestRotationCrashBeforeCoreCommitKeepsCurrentKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)
	core.commit = false
	apiURL := core.server.URL + "/api/v1"

	if _, err := rotateCredential(context.Background(), dir, apiURL, id, nil, time.Now()); err == nil {
		t.Fatal("rotation without a response reported success")
	}
	if !pendingExists(dir) {
		t.Fatal("pending key was discarded on an ambiguous failure")
	}
	next, err := resolvePendingRotation(context.Background(), dir, apiURL, id, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if next.CredentialID != id.CredentialID || pendingExists(dir) {
		t.Fatal("unknown pending key was not discarded")
	}
}

func TestResolvePendingRotationKeepsBothKeysWhileCoreIsUnreachable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)
	core.dropResponse = true
	apiURL := core.server.URL + "/api/v1"
	if _, err := rotateCredential(context.Background(), dir, apiURL, id, nil, time.Now()); err == nil {
		t.Fatal("expected a failed rotation")
	}
	core.server.Close()
	if _, err := resolvePendingRotation(context.Background(), dir, apiURL, id, nil); err == nil {
		t.Fatal("resolved a rotation without reaching Core")
	}
	if !pendingExists(dir) {
		t.Fatal("pending key was discarded while Core was unreachable")
	}
}

func TestRefusedRotationDiscardsPendingKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)
	core.revoked[id.CredentialID] = true

	_, err := rotateCredential(context.Background(), dir, core.server.URL+"/api/v1", id, nil, time.Now())
	if !isAPIError(err, agentproto.CodeCredentialRevoked) {
		t.Fatalf("got %v, want a revoked-credential error", err)
	}
	if pendingExists(dir) {
		t.Fatal("pending key kept although Core refused the rotation")
	}
}

func TestRotateAndSettleAgainstOlderCore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	core := newRotationCore(t, id)
	core.legacy = true

	if _, err := rotateAndSettle(context.Background(), dir, core.server.URL+"/api/v1", id, nil); err == nil {
		t.Fatal("rotation against a Core without the endpoint reported success")
	}
	if pendingExists(dir) {
		t.Fatal("pending key kept although Core still accepts the current key")
	}
	if current, _ := loadIdentity(dir); current.CredentialID != id.CredentialID {
		t.Fatal("identity changed after a failed rotation")
	}
}

func TestStalePendingRotationIsDiscarded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	id := enrolledIdentity(t, dir)
	stale := []byte(`{"version":1,"agentId":"` + id.AgentID + `","previousCredentialId":"00000000-0000-4000-8000-000000000000",` +
		`"credentialId":"10000000-0000-4000-8000-000000000000","privateKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","createdAt":"2026-01-01T00:00:00Z"}`)
	if err := writePrivateFile(dir, pendingRotationFileName, stale); err != nil {
		t.Fatal(err)
	}
	next, err := resolvePendingRotation(context.Background(), dir, "http://127.0.0.1:1/api/v1", id, nil)
	if err != nil || next.CredentialID != id.CredentialID {
		t.Fatalf("stale pending key: %+v, %v", next, err)
	}
	if _, err := os.Stat(filepath.Join(dir, pendingRotationFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("stale pending key was not removed")
	}
}

func TestCredentialAgeFallsBackToEnrollment(t *testing.T) {
	now := time.Now()
	id := &identity{EnrolledAt: now.Add(-72 * time.Hour)}
	if age := id.credentialAge(now); age != 72*time.Hour {
		t.Fatalf("age = %v", age)
	}
	id.CredentialCreatedAt = now.Add(-time.Hour)
	if age := id.credentialAge(now); age != time.Hour {
		t.Fatalf("age = %v", age)
	}
}

func TestNewCredentialIDIsCoreUUID(t *testing.T) {
	for range 100 {
		value, err := newCredentialID()
		if err != nil || len(value) != 36 || value[14] != '4' || !isLowerUUID(value) {
			t.Fatalf("credential ID %q (%v)", value, err)
		}
	}
}

func isLowerUUID(value string) bool {
	request := agentproto.RotateCredentialRequest{CredentialID: value}
	_, key, _ := ed25519.GenerateKey(nil)
	signed := agentproto.SignRotation("a", "b", request.CredentialID, key, time.Now())
	_, err := agentproto.VerifyRotation(signed, "a", "b", time.Now())
	return err == nil
}
