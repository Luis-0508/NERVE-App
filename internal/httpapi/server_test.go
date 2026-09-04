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

