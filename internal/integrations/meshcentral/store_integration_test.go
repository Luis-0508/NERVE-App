//go:build integration

package meshcentral

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

type fakeNode struct{ id, name, mac string }

// listingServer serves whatever nodes currently holds; closeWith, when set,
// replaces the listing with a close message.
func listingServer(t *testing.T, nodes *[]fakeNode, closeWith *string) *httptest.Server {
	return httptest.NewServer(fakeControl{t: t, reply: func(request map[string]any) []string {
		if *closeWith != "" {
			return []string{*closeWith}
		}
		switch request["action"] {
		case "nodes":
			items := []string{}
			for _, node := range *nodes {
				items = append(items, fmt.Sprintf(`{"_id":%q,"name":%q,"conn":1}`, node.id, node.name))
			}
			return []string{`{"action":"nodes","responseid":"nerve","nodes":{"mesh//g":[` + strings.Join(items, ",") + `]}}`}
		case "getnetworkinfo":
			for _, node := range *nodes {
				if node.id == request["nodeid"] && node.mac != "" {
					return []string{fmt.Sprintf(`{"action":"getnetworkinfo","nodeid":%q,"netif2":{"eth0":[{"mac":%q,"address":"192.0.2.99"}]}}`, node.id, node.mac)}
				}
			}
			return []string{fmt.Sprintf(`{"action":"getnetworkinfo","nodeid":%q,"netif":null,"netif2":null}`, request["nodeid"])}
		case "lastconnect":
			return []string{fmt.Sprintf(`{"action":"lastconnect","nodeid":%q,"noinfo":true,"result":"No data"}`, request["nodeid"])}
		}
		return nil
	}})
}

func TestSyncCandidatesAndConfirmedBindings(t *testing.T) {
	pool := testsupport.MigratedPool(t, "meshcentral")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := Actor{UserID: adminID}
	nas := testsupport.CreateDevice(t, pool, "nas", "02:00:00:00:00:10", "192.0.2.10")
	printer := testsupport.CreateDevice(t, pool, "printer", "02:00:00:00:00:20", "192.0.2.20")

	nodes := []fakeNode{{"node//nasnode", "NAS", "02:00:00:00:00:10"}, {"node//printernode", "printer", ""}, {"node//other", "laptop", "02:00:00:00:00:99"}}
	closeWith := ""
	server := listingServer(t, &nodes, &closeWith)
	defer server.Close()
	store := NewStore(pool, New(Config{URL: server.URL, Username: "nerve-svc", Password: "integration-secret", Timeout: 5 * time.Second}))
	provider := Provider{Store: store}
	endpoints := func(deviceID string) []remote.Endpoint {
		t.Helper()
		items, err := provider.EndpointsForDevice(ctx, deviceID)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}

	if err := store.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Status(ctx); err != nil || status.NodeCount != 3 || status.LastSuccessAt == nil || status.LastError != "" {
		t.Fatalf("unexpected sync status %+v, %v", status, err)
	}

	// MAC plus hostname evidence makes a candidate; nothing is bound automatically.
	got := endpoints(nas)
	if len(got) != 1 || got[0].EndpointID != "node//nasnode" || got[0].Association != AssociationCandidate || len(got[0].Evidence) != 2 {
		t.Fatalf("unexpected NAS endpoints %+v", got)
	}
	for _, capability := range got[0].Capabilities {
		if capability.Executable {
			t.Fatalf("MeshCentral capability %s must not be executable yet", capability.Name)
		}
	}
	if len(got[0].Capabilities) != 3 {
		t.Fatalf("expected three interactive capabilities, got %+v", got[0].Capabilities)
	}

	// Hostname alone is weak and the binding is refused and audited.
	got = endpoints(printer)
	if len(got) != 1 || got[0].Association != AssociationWeak {
		t.Fatalf("unexpected printer endpoints %+v", got)
	}
	if err := store.Confirm(ctx, printer, "node//printernode", actor); !errors.Is(err, ErrWeakEvidence) {
		t.Fatalf("expected weak evidence refusal, got %v", err)
	}
	var denied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'meshcentral.binding.confirmed' AND result = 'denied' AND error_code = 'weak_evidence'`).Scan(&denied); err != nil || denied != 1 {
		t.Fatalf("expected one denied audit record, got %d, %v", denied, err)
	}

	// A node whose MAC belongs to another device cannot be bound here either.
	if err := store.Confirm(ctx, printer, "node//nasnode", actor); !errors.Is(err, ErrWeakEvidence) {
		t.Fatalf("expected refusal for a node without matching MAC, got %v", err)
	}

	if err := store.Confirm(ctx, nas, "node//nasnode", actor); err != nil {
		t.Fatal(err)
	}
	if got = endpoints(nas); len(got) != 1 || got[0].Association != AssociationConfirmed || !got[0].Online {
		t.Fatalf("binding not confirmed %+v", got)
	}
	if err := store.Confirm(ctx, nas, "node//nasnode", actor); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("expected already bound, got %v", err)
	}

	// A reinstall produces a new node ID. It does not inherit the binding: the
	// old node is reported absent and the new one is only a candidate.
	nodes = []fakeNode{{"node//nasreinstalled", "NAS", "02:00:00:00:00:10"}, {"node//printernode", "printer", ""}}
	if err := store.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	got = endpoints(nas)
	if len(got) != 2 || got[0].EndpointID != "node//nasnode" || got[0].Association != AssociationConfirmed || got[0].Status != "absent" || got[0].Online {
		t.Fatalf("old binding not reported absent %+v", got)
	}
	if got[1].EndpointID != "node//nasreinstalled" || got[1].Association != AssociationCandidate {
		t.Fatalf("reinstalled node inherited the binding %+v", got[1])
	}
	if err := store.Confirm(ctx, nas, "node//nasnode", actor); !errors.Is(err, ErrNodeNotFound) && !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("absent node must not be re-bound, got %v", err)
	}

	if err := store.Unbind(ctx, nas, "node//nasnode", actor); err != nil {
		t.Fatal(err)
	}
	if err := store.Unbind(ctx, nas, "node//nasnode", actor); !errors.Is(err, ErrNotBound) {
		t.Fatalf("expected not bound, got %v", err)
	}
	if err := store.Confirm(ctx, nas, "node//nasreinstalled", actor); err != nil {
		t.Fatal(err)
	}

	// A failed sync keeps the last listing and records a safe error.
	closeWith = `{"action":"close","cause":"noauth"}`
	if err := store.Sync(ctx); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
	status, err := store.Status(ctx)
	if err != nil || status.NodeCount != 2 || !strings.Contains(status.LastError, "authentication") || strings.Contains(status.LastError, "integration-secret") {
		t.Fatalf("unexpected failure status %+v, %v", status, err)
	}
	if got = endpoints(nas); len(got) != 1 || got[0].EndpointID != "node//nasreinstalled" || got[0].Status != "present" {
		t.Fatalf("a failed sync changed the node state %+v", got)
	}
}
