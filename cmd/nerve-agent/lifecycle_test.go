package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

func TestResolveSettingsPrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := saveConfig(dir, agentConfig{APIURL: "https://core.example/api/v1/", DisabledCapabilities: []string{"software.list"}, LogLevel: "warn"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NERVE_AGENT_API_URL", "")
	t.Setenv("NERVE_AGENT_DISABLED_CAPABILITIES", "process.list")
	t.Setenv("NERVE_AGENT_LOG_LEVEL", "")
	got, err := resolveSettings(dir, &identity{APIURL: "http://enrolled/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "https://core.example/api/v1" || got.LogLevel != slog.LevelWarn || strings.Join(got.DisabledCapabilities, ",") != "software.list,process.list" {
		t.Fatalf("config not applied: %+v", got)
	}
	t.Setenv("NERVE_AGENT_API_URL", "https://moved.example/api/v1")
	if got, _ := resolveSettings(dir, nil); got.APIURL != "https://moved.example/api/v1" {
		t.Fatalf("environment did not override: %s", got.APIURL)
	}
	// The identity's URL is the fallback when nothing else is configured.
	t.Setenv("NERVE_AGENT_API_URL", "")
	if got, _ := resolveSettings(t.TempDir(), &identity{APIURL: "http://enrolled/api/v1"}); got.APIURL != "http://enrolled/api/v1" {
		t.Fatalf("identity fallback: %s", got.APIURL)
	}
	for _, bad := range []string{"ftp://core/api", "core.example/api/v1", "https://user:pass@core/api/v1", "https://core/api/v1?x=1"} {
		t.Setenv("NERVE_AGENT_API_URL", bad)
		if _, err := resolveSettings(dir, nil); err == nil {
			t.Fatalf("accepted API URL %q", bad)
		}
	}
	t.Setenv("NERVE_AGENT_API_URL", "")
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"apiUrl":"https://core/api/v1","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSettings(dir, nil); err == nil {
		t.Fatal("unknown configuration keys were accepted")
	}
}

func TestCapabilitiesWithoutDisabled(t *testing.T) {
	t.Setenv("NERVE_AGENT_DISABLED_CAPABILITIES", "")
	for _, name := range capabilitiesWithout([]string{agentproto.CapabilitySystemInfo})() {
		if name == agentproto.CapabilitySystemInfo {
			t.Fatal("disabled capability advertised")
		}
	}
}

func TestTransportRejectsInvalidCA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTransport(path); err == nil {
		t.Fatal("invalid CA file accepted")
	}
	transport, err := newTransport("")
	if err != nil || transport.TLSClientConfig.MinVersion < 0x0303 || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatalf("unsafe default transport: %v", err)
	}
}

func TestRotatingFileKeepsBoundedHistory(t *testing.T) {
	dir := t.TempDir()
	file, err := openRotatingFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	file.maxSize = 100
	line := []byte(strings.Repeat("x", 39) + "\n")
	for range 20 {
		if _, err := file.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()
	entries, _ := os.ReadDir(dir)
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
		info, _ := entry.Info()
		if info.Size() > 100 {
			t.Fatalf("%s exceeds the size limit: %d", entry.Name(), info.Size())
		}
	}
	if strings.Join(names, ",") != "agent.log,agent.log.1,agent.log.2,agent.log.3" {
		t.Fatalf("unexpected log files %v", names)
	}
}

func TestClockStatusMatchesCoreLimit(t *testing.T) {
	for skew, want := range map[time.Duration]string{
		30 * time.Second:                         "ok",
		-90 * time.Second:                        "warn",
		agentproto.MaxClockSkew:                  "warn",
		agentproto.MaxClockSkew + time.Second:    "fail",
		-(agentproto.MaxClockSkew + time.Second): "fail",
	} {
		if got := clockStatus(skew); got != want {
			t.Fatalf("skew %s: %s, want %s", skew, got, want)
		}
	}
}

func TestRotatingFileKeepsLoggingWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory in the way makes the rename fail on every platform,
	// like a log viewer holding agent.log open on Windows.
	blocker := filepath.Join(dir, logFileName+".1")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := openRotatingFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.keep = 1
	file.maxSize = 100
	line := []byte(strings.Repeat("x", 39) + "\n")
	for i := range 6 {
		if _, err := file.Write(line); err != nil {
			t.Fatalf("write %d after a failed rotation: %v", i, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, logFileName))
	if err != nil || len(content) != 6*len(line) {
		t.Fatalf("log lost lines: %d bytes, %v", len(content), err)
	}
}

func TestPrepareServiceStateCopiesCAFile(t *testing.T) {
	stateDir := t.TempDir()
	source := filepath.Join(t.TempDir(), "private-ca.pem")
	caPEM := testCertificatePEM(t)
	if err := os.WriteFile(source, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareServiceState(installOptions{APIURL: "https://core.example/api/v1", CAFile: source, StateDir: stateDir}); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateDir, caFileName); config.CAFile != want {
		t.Fatalf("config points at %s, want the private copy %s", config.CAFile, want)
	}
	copied, err := os.ReadFile(config.CAFile)
	if err != nil || string(copied) != string(caPEM) {
		t.Fatalf("CA copy differs: %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := newTransport(config.CAFile); err != nil {
		t.Fatalf("copy unusable without the original: %v", err)
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NERVE test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(nil, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestWatchIdentityDetectsReenrollment(t *testing.T) {
	dir := t.TempDir()
	_, key, _ := ed25519.GenerateKey(nil)
	if err := saveIdentity(dir, &identity{AgentID: "a1", CredentialID: "c1", key: key, coreKey: testCorePublic}); err != nil {
		t.Fatal(err)
	}
	identityPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { identityPollInterval = 15 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{})
	go func() {
		watchIdentity(ctx, dir, "c1", func() { close(changed) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	// Rewriting the same credential is not a change.
	if err := saveIdentity(dir, &identity{AgentID: "a1", CredentialID: "c1", key: key, coreKey: testCorePublic}); err != nil {
		t.Fatal(err)
	}
	_, next, _ := ed25519.GenerateKey(nil)
	time.Sleep(20 * time.Millisecond)
	if err := saveIdentity(dir, &identity{AgentID: "a1", CredentialID: "c2", key: next, coreKey: testCorePublic}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("re-enrollment not detected")
	}
}

func TestShutdownReportsInterruptedCommandAndStoppingHeartbeat(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	var mu sync.Mutex
	var results []agentproto.CommandResult
	var stopping []bool
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case route == "/agent/heartbeat":
			var request agentproto.HeartbeatRequest
			_ = json.Unmarshal(body, &request)
			if request.Runtime == nil || request.Runtime.RunMode != agentproto.RunModeSystemd || request.Runtime.Validate(time.Now()) != nil {
				return 400, nil
			}
			stopping = append(stopping, request.Runtime.Stopping)
			return 200, agentproto.HeartbeatResponse{}
		case strings.HasSuffix(route, "/start"):
			return 200, map[string]string{}
		case strings.HasSuffix(route, "/result"):
			var result agentproto.CommandResult
			_ = json.Unmarshal(body, &result)
			results = append(results, result)
			return 200, agentproto.ResultAck{}
		}
		return 404, nil
	})
	defer server.Close()
	m := &manager{client: newManagementClient(server.URL+"/api/v1", &identity{CredentialID: "cred", key: private, coreKey: testCorePublic}),
		agentID: "agent-1", logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		capabilities: func() []string { return []string{agentproto.CapabilitySystemInfo} }, facts: collectHostFacts,
		runtime: runtimeReporter(agentproto.RunModeSystemd, time.Now())}
	ctx, stop := context.WithCancel(context.Background())
	m.collectors = map[string]collector{agentproto.CapabilitySystemInfo: func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		stop() // the service stops while the command runs
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m.execute(ctx, agentproto.Command{ID: "cmd-1", AgentID: "agent-1", Type: agentproto.CapabilitySystemInfo, TimeoutSeconds: 30,
		MaxOutputBytes: 1024, Deadline: time.Now().Add(time.Minute)})
	m.reportStopping()
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 1 || results[0].FailureCode != "agent_stopped" {
		t.Fatalf("interrupted command not reported: %+v", results)
	}
	if len(stopping) != 1 || !stopping[0] {
		t.Fatalf("stopping heartbeat not sent: %v", stopping)
	}
}

func TestHeartbeatFallsBackForCoreWithoutRuntimeSupport(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	var accepted, rejected int
	server := fakeCore(t, &public, func(route string, body []byte) (int, any) {
		// Mimic an older Core: strict decoding without the runtime field.
		var request struct {
			Host         agentproto.HostFacts `json:"host"`
			Capabilities []string             `json:"capabilities"`
			LastError    string               `json:"lastError,omitempty"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			rejected++
			return 400, map[string]any{"error": map[string]string{"code": "invalid_request", "message": "invalid JSON request: " + err.Error()}}
		}
		accepted++
		return 200, agentproto.HeartbeatResponse{}
	})
	defer server.Close()
	m := &manager{client: newManagementClient(server.URL+"/api/v1", &identity{CredentialID: "cred", key: private, coreKey: testCorePublic}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), capabilities: func() []string { return nil }, facts: collectHostFacts,
		runtime: runtimeReporter(agentproto.RunModeForeground, time.Now())}
	for range 2 {
		if err := m.heartbeat(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if rejected != 1 || accepted != 2 {
		t.Fatalf("fallback: rejected %d, accepted %d", rejected, accepted)
	}
}
