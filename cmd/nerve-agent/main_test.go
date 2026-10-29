package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/discovery"
)

func TestEnumerateIPv4SubnetSkipsNetworkAndBroadcast(t *testing.T) {
	t.Parallel()
	addresses := enumerate(netip.MustParsePrefix("192.0.2.0/30"))
	if len(addresses) != 2 || addresses[0].String() != "192.0.2.1" || addresses[1].String() != "192.0.2.2" {
		t.Fatalf("unexpected addresses: %v", addresses)
	}
}

func TestEnumerateIPv6Subnet(t *testing.T) {
	t.Parallel()
	addresses := enumerate(netip.MustParsePrefix("fd00::/126"))
	if len(addresses) != 4 {
		t.Fatalf("got %d addresses, want 4", len(addresses))
	}
}

func TestHeartbeatsContinueDuringScanAndScansStaySerial(t *testing.T) {
	t.Parallel()

	var heartbeatCount atomic.Int32
	var claimCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/discovery/agent/heartbeat":
			if heartbeatCount.Add(1) > 1 {
				http.Error(w, "temporary heartbeat failure", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/discovery/agent/jobs/claim":
			claimNumber := claimCount.Add(1)
			if claimNumber <= 2 {
				_ = json.NewEncoder(w).Encode(discovery.Job{ID: fmt.Sprintf("scan-%d", claimNumber), CIDR: "192.0.2.0/30"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	executeStarted := make(chan struct{})
	allExecutionsFinished := make(chan struct{})
	var activeScans atomic.Int32
	var maximumActive atomic.Int32
	var executeCount atomic.Int32
	a := &agent{
		send: legacyDiscoverySender(server.URL+"/api/v1", "test-token", "test-agent", &http.Client{Timeout: time.Second}), id: "test-agent",
		hostname: "test-host", logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		heartbeatInterval: 20 * time.Millisecond, claimInterval: 5 * time.Millisecond,
		executeFunc: func(ctx context.Context, _ discovery.Job) error {
			executionNumber := executeCount.Add(1)
			active := activeScans.Add(1)
			for observedMaximum := maximumActive.Load(); active > observedMaximum; observedMaximum = maximumActive.Load() {
				if maximumActive.CompareAndSwap(observedMaximum, active) {
					break
				}
			}
			if executionNumber == 1 {
				close(executeStarted)
			}
			select {
			case <-ctx.Done():
				activeScans.Add(-1)
				return ctx.Err()
			case <-time.After(80 * time.Millisecond):
			}
			activeScans.Add(-1)
			if executionNumber == 2 {
				close(allExecutionsFinished)
			}
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx) }()
	select {
	case <-executeStarted:
	case <-time.After(time.Second):
		t.Fatal("scan did not start")
	}
	select {
	case <-allExecutionsFinished:
	case <-time.After(time.Second):
		t.Fatal("scan did not finish")
	}
	if got := heartbeatCount.Load(); got < 6 {
		t.Fatalf("got %d heartbeats during scans, want at least 6", got)
	}
	if got := maximumActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent scans = %d, want 1", got)
	}
	if got := executeCount.Load(); got != 2 {
		t.Fatalf("executed %d scans, want 2", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("agent returned unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop after context cancellation")
	}
}
