package agentproto

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

func TestCoreResponseSignatureBindsRequestStatusAndBody(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	header := http.Header{}
	body := []byte(`{"id":"c1","type":"system.info"}`)
	SignCoreResponse(header, private, "request-signature-1", 200, body)
	if err := VerifyCoreResponse(public, header, "request-signature-1", 200, body); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	attacker, attackerKey, _ := ed25519.GenerateKey(nil)
	forged := http.Header{}
	SignCoreResponse(forged, attackerKey, "request-signature-1", 200, body)
	for name, check := range map[string]func() error{
		"other request": func() error { return VerifyCoreResponse(public, header, "request-signature-2", 200, body) },
		"status":        func() error { return VerifyCoreResponse(public, header, "request-signature-1", 401, body) },
		"body": func() error {
			return VerifyCoreResponse(public, header, "request-signature-1", 200, []byte(`{"id":"evil"}`))
		},
		"forged key": func() error { return VerifyCoreResponse(public, forged, "request-signature-1", 200, body) },
		"unsigned":   func() error { return VerifyCoreResponse(public, http.Header{}, "request-signature-1", 200, body) },
	} {
		if check() == nil {
			t.Fatalf("response accepted after changing %s", name)
		}
	}
	// The public key in the header is accepted only if it matches the token fingerprint.
	if key, err := CoreKeyFromResponse(header, CoreFingerprint(public)); err != nil || !key.Equal(public) {
		t.Fatalf("genuine key rejected: %v", err)
	}
	if _, err := CoreKeyFromResponse(forged, CoreFingerprint(public)); err == nil {
		t.Fatal("attacker key accepted against the pinned fingerprint")
	}
	_ = attacker
}

func TestParseCoreSigningKey(t *testing.T) {
	seed := make([]byte, 32)
	seed[0] = 1
	key, err := ParseCoreSigningKey(hex.EncodeToString(seed))
	if err != nil || !key.Equal(ed25519.NewKeyFromSeed(seed)) {
		t.Fatalf("hex seed: %v", err)
	}
	for _, invalid := range []string{"", "abcd", "change-me"} {
		if _, err := ParseCoreSigningKey(invalid); err == nil {
			t.Fatalf("invalid key %q accepted", invalid)
		}
	}
}

func TestAgentRuntimeValidate(t *testing.T) {
	now := time.Now()
	valid := AgentRuntime{RunMode: RunModeWindowsService, StartedAt: now.Unix(), HostBootAt: now.Add(-time.Hour).Unix()}
	if err := valid.Validate(now); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []AgentRuntime{
		{RunMode: "cron", StartedAt: now.Unix()},
		{RunMode: RunModeSystemd},
		{RunMode: RunModeSystemd, StartedAt: now.Add(time.Hour).Unix()},
		{RunMode: RunModeSystemd, StartedAt: now.Unix(), HostBootAt: -1},
	} {
		if bad.Validate(now) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
