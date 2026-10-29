package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nerve-labs/nerve/internal/config"
)

func TestLiveness(t *testing.T) {
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000"}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected security headers")
	}
}

func TestNativeIngestIsNotOnBrowserListener(t *testing.T) {
	server := New(nil, config.Config{TrafficCaptureMode: "windows-native", SensorToken: strings.Repeat("a", 64)}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	web := httptest.NewRecorder()
	server.Handler().ServeHTTP(web, httptest.NewRequest("POST", "/v1/snapshot", strings.NewReader("{}")))
	if web.Code != 404 {
		t.Fatal("sensor ingress exposed on web listener")
	}
	sensor := httptest.NewRecorder()
	server.SensorHandler().ServeHTTP(sensor, httptest.NewRequest("POST", "/v1/snapshot", strings.NewReader("{}")))
	if sensor.Code != 401 {
		t.Fatal("unauthenticated sensor report accepted")
	}
}

func TestAPIRoot(t *testing.T) {
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000"}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	for _, path := range []string{"/api/v1", "/api/v1/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("expected status 200 for %s, got %d", path, response.Code)
		}
	}
}

func TestTrustedProxyAllowsLANSameOrigin(t *testing.T) {
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", TrustProxy: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	request.Header.Set("Origin", "http://192.168.1.25:3000")
	request.Header.Set("X-Forwarded-Host", "192.168.1.25:3000")
	request.Header.Set("X-Forwarded-Proto", "http")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", response.Code)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "http://192.168.1.25:3000" {
		t.Fatal("expected the matching LAN origin to be allowed")
	}
}

func TestTrustedProxyRejectsMismatchedOrigin(t *testing.T) {
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", TrustProxy: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	request.Header.Set("Origin", "http://attacker.example")
	request.Header.Set("X-Forwarded-Host", "192.168.1.25:3000")
	request.Header.Set("X-Forwarded-Proto", "http")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", response.Code)
	}
}

func TestStatusDegradesWithoutDatabase(t *testing.T) {
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000"}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "degraded" {
		t.Fatalf("expected degraded status, got %q", body.Status)
	}
}

func TestDiscoveryAgentRequiresConfiguredBearerToken(t *testing.T) {
	t.Parallel()
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", DiscoveryAgentToken: "local-secret", DiscoveryLegacyToken: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	for _, authorization := range []string{"", "Bearer wrong-secret"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/agent/heartbeat", strings.NewReader(`{"id":"test-agent"}`))
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401 for %q, got %d", authorization, response.Code)
		}
	}
}

func TestDiscoveryLegacyTokenCanBeDisabled(t *testing.T) {
	t.Parallel()
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", DiscoveryAgentToken: "local-secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/agent/jobs/claim", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("X-NERVE-Agent-ID", "host-linux")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "legacy_discovery_disabled") {
		t.Fatalf("disabled legacy path: %d %s", response.Code, response.Body.String())
	}
}

func TestDiscoveryLegacyWorkerCannotUseAgentID(t *testing.T) {
	t.Parallel()
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", DiscoveryAgentToken: "local-secret", DiscoveryLegacyToken: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	for _, route := range []string{"/api/v1/discovery/agent/jobs/claim", "/api/v1/discovery/agent/heartbeat"} {
		request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer local-secret")
		// An enrolled agent's worker row is keyed by the agent UUID.
		request.Header.Set("X-NERVE-Agent-ID", "3f2b8c1e-4d5a-4b6c-8d7e-9f0a1b2c3d4e")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s with a UUID worker ID = %d, want 400", route, response.Code)
		}
	}
}

func TestSignedDiscoveryRequestIsNeverDowngradedToToken(t *testing.T) {
	t.Parallel()
	// Without a Core signing key the signed path is unavailable; a valid bearer
	// token on the same request must not open the legacy path instead.
	server := New(nil, config.Config{AllowedOrigin: "http://localhost:3000", DiscoveryAgentToken: "local-secret", DiscoveryLegacyToken: true}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/agent/jobs/claim", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("X-NERVE-Agent-ID", "host-linux")
	request.Header.Set("X-NERVE-Signature", "forged")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusNoContent || response.Code == http.StatusOK || response.Code == http.StatusInternalServerError {
		t.Fatalf("signed request fell back to the token path: %d %s", response.Code, response.Body.String())
	}
}
