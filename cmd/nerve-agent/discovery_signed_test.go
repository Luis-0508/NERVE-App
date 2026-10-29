package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/discovery"
)

func TestSignedDiscoveryWorkerSignsRequestsAndVerifiesCore(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	var heartbeats, claims atomic.Int32
	server := fakeCore(t, &public, func(route string, _ []byte) (int, any) {
		switch route {
		case "/discovery/agent/heartbeat":
			heartbeats.Add(1)
			return http.StatusNoContent, nil
		case "/discovery/agent/jobs/claim":
			if claims.Add(1) == 1 {
				return http.StatusOK, discovery.Job{ID: "scan-1", CIDR: "192.0.2.0/30"}
			}
			return http.StatusNoContent, nil
		}
		return http.StatusNotFound, nil
	})
	defer server.Close()
	id := &identity{AgentID: "agent-1", CredentialID: "cred-1", key: private, coreKey: testCorePublic}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// fakeCore answers 401 to anything not signed with the agent key.
	worker := newSignedDiscoveryAgent(newManagementClient(server.URL+"/api/v1", id), logger)
	if err := worker.heartbeat(context.Background(), ""); err != nil {
		t.Fatalf("signed heartbeat: %v", err)
	}
	job, err := worker.claim(context.Background())
	if err != nil || job.ID != "scan-1" {
		t.Fatalf("signed claim = %+v, %v", job, err)
	}
	if _, err := worker.claim(context.Background()); !errors.Is(err, discovery.ErrNoJob) {
		t.Fatalf("empty queue: %v", err)
	}

	// A job whose answer is not signed by the pinned Core key is never run.
	otherCore, _, _ := ed25519.GenerateKey(nil)
	claims.Store(0)
	forged := &identity{AgentID: "agent-1", CredentialID: "cred-1", key: private, coreKey: otherCore}
	worker = newSignedDiscoveryAgent(newManagementClient(server.URL+"/api/v1", forged), logger)
	var executed atomic.Bool
	worker.executeFunc = func(context.Context, discovery.Job) error {
		executed.Store(true)
		return nil
	}
	worker.claimInterval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = worker.run(ctx)
	if executed.Load() {
		t.Fatal("a job with an unverifiable Core response was executed")
	}
	if claims.Load() == 0 {
		t.Fatal("the worker never tried to claim")
	}
	if heartbeats.Load() < 2 {
		t.Fatalf("heartbeats = %d, want the signed worker's heartbeats", heartbeats.Load())
	}
}
