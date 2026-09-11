package httpapi

import (
	"testing"
	"time"
)

func TestAllowLoginPrunesExpiredWindows(t *testing.T) {
	now := time.Now()
	server := &Server{loginAttempts: map[string]*rateWindow{
		"192.0.2.10": {started: now.Add(-16 * time.Minute), count: 8},
		"192.0.2.11": {started: now.Add(-5 * time.Minute), count: 2},
	}}

	if !server.allowLogin("192.0.2.12") {
		t.Fatal("new address was unexpectedly rate limited")
	}
	if _, exists := server.loginAttempts["192.0.2.10"]; exists {
		t.Fatal("expired rate-limit window was not pruned")
	}
	if _, exists := server.loginAttempts["192.0.2.11"]; !exists {
		t.Fatal("active rate-limit window was pruned")
	}
}

func TestAllowLoginLimitsCurrentWindow(t *testing.T) {
	server := &Server{loginAttempts: make(map[string]*rateWindow)}
	for attempt := 1; attempt <= 8; attempt++ {
		if !server.allowLogin("192.0.2.20") {
			t.Fatalf("attempt %d was unexpectedly rejected", attempt)
		}
	}
	if server.allowLogin("192.0.2.20") {
		t.Fatal("ninth attempt was unexpectedly allowed")
	}
}
