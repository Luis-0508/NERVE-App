package agentproto

import (
	"bytes"
	"crypto/ed25519"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func signedRequest(t *testing.T, key ed25519.PrivateKey, method, target string, body []byte, now time.Time) SignedHeaders {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if err := Sign(request, "cred-1", key, body, now); err != nil {
		t.Fatal(err)
	}
	headers, err := ParseHeaders(request.Header)
	if err != nil {
		t.Fatal(err)
	}
	return headers
}

func TestSignatureRoundTripBindsRequest(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"a":1}`)
	headers := signedRequest(t, private, "POST", "http://core/api/v1/agent/heartbeat", body, now)
	if err := headers.Verify(public, "POST", "/agent/heartbeat", body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	for name, check := range map[string]func() error{
		"body":   func() error { return headers.Verify(public, "POST", "/agent/heartbeat", []byte(`{"a":2}`)) },
		"route":  func() error { return headers.Verify(public, "POST", "/agent/commands/claim", body) },
		"method": func() error { return headers.Verify(public, "PUT", "/agent/heartbeat", body) },
		"credential": func() error {
			other := headers
			other.CredentialID = "cred-2"
			return other.Verify(public, "POST", "/agent/heartbeat", body)
		},
		"key": func() error {
			otherPublic, _, _ := ed25519.GenerateKey(nil)
			return headers.Verify(otherPublic, "POST", "/agent/heartbeat", body)
		},
	} {
		if check() == nil {
			t.Fatalf("signature accepted after changing %s", name)
		}
	}
}

func TestSignatureQueryIsSigned(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	headers := signedRequest(t, private, "GET", "http://core/api/v1/agent/x?wait=5", nil, now)
	if err := headers.Verify(public, "GET", "/agent/x?wait=5", nil); err != nil {
		t.Fatal(err)
	}
	if headers.Verify(public, "GET", "/agent/x?wait=50", nil) == nil {
		t.Fatal("query string is not covered by the signature")
	}
}

func TestClockSkew(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	signedAt := time.Unix(1_800_000_000, 0)
	headers := signedRequest(t, private, "POST", "http://core/api/v1/agent/heartbeat", nil, signedAt)
	if err := headers.CheckTimestamp(signedAt.Add(MaxClockSkew - time.Second)); err != nil {
		t.Fatalf("timestamp inside the window rejected: %v", err)
	}
	if headers.CheckTimestamp(signedAt.Add(MaxClockSkew+time.Second)) == nil || headers.CheckTimestamp(signedAt.Add(-MaxClockSkew-time.Second)) == nil {
		t.Fatal("timestamp outside the window accepted")
	}
}

func TestParseHeadersRejectsMalformedInput(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(nil)
	request := httptest.NewRequest("POST", "http://core/api/v1/agent/heartbeat", nil)
	if _, err := ParseHeaders(request.Header); err != ErrMissingSignature {
		t.Fatalf("missing headers: got %v", err)
	}
	if err := Sign(request, "cred-1", private, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	for header, value := range map[string]string{HeaderNonce: "abcd", HeaderTimestamp: "soon", HeaderSignature: "not-base64!", HeaderCredential: strings.Repeat("x", 65)} {
		clone := request.Clone(request.Context())
		clone.Header.Set(header, value)
		if _, err := ParseHeaders(clone.Header); err != ErrMalformed {
			t.Fatalf("%s=%q: got %v, want ErrMalformed", header, value, err)
		}
	}
}

func TestEnrollmentTokenFormat(t *testing.T) {
	secret, err := NewEnrollmentSecret()
	if err != nil {
		t.Fatal(err)
	}
	id := "3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e"
	corePublic, _, _ := ed25519.GenerateKey(nil)
	fingerprint := CoreFingerprint(corePublic)
	parsed, err := ParseEnrollmentToken(FormatEnrollmentToken(id, secret, fingerprint))
	if err != nil || parsed.ID != id || len(parsed.Secret) != 32 || parsed.CoreFingerprint != fingerprint {
		t.Fatalf("round trip failed: %+v %v", parsed, err)
	}
	for _, token := range []string{
		"", "nerve-enroll-v2." + id, "nerve-enroll-v2." + id + "." + secret,
		"nerve-enroll-v1." + id + "." + secret + "." + fingerprint,
		"nerve-enroll-v2.not-a-uuid." + secret + "." + fingerprint, "nerve-enroll-v2." + id + ".short." + fingerprint,
		"nerve-enroll-v2." + strings.ToUpper(id) + "." + secret + "." + fingerprint,
		"nerve-enroll-v2." + id + "." + secret + ".shortfingerprint",
	} {
		if _, err := ParseEnrollmentToken(token); err == nil {
			t.Fatalf("malformed token accepted: %q", token)
		}
	}
	if parsed.Deployment {
		t.Fatal("an enrollment token was parsed as a deployment key")
	}
	key, err := ParseEnrollmentToken(FormatDeploymentKey(id, secret, fingerprint))
	if err != nil || !key.Deployment || key.ID != id || key.CoreFingerprint != fingerprint {
		t.Fatalf("deployment key round trip failed: %+v %v", key, err)
	}
	if _, err := ParseEnrollmentToken("nerve-deploy-v2." + id + "." + secret + "." + fingerprint); err == nil {
		t.Fatal("unknown deployment key version accepted")
	}
}

func TestHostFactsValidation(t *testing.T) {
	valid := HostFacts{Hostname: "host", OS: "linux", Architecture: "amd64", AgentVersion: "dev"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	tooLong := valid
	tooLong.Hostname = strings.Repeat("h", MaxFactLength+1)
	tooMany := valid
	tooMany.Interfaces = make([]InterfaceFact, MaxInterfaces+1)
	missing := valid
	missing.Hostname = " "
	for _, facts := range []HostFacts{tooLong, tooMany, missing} {
		if facts.Validate() == nil {
			t.Fatal("invalid host facts accepted")
		}
	}
}
