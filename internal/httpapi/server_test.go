package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
