//go:build integration

package devices_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var value int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return value
}

func addIdentity(t *testing.T, pool *pgxpool.Pool, deviceID, identityType, value, source string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO device_identities (device_id, identity_type, value, source, confidence)
		VALUES ($1::uuid, $2, $3, $4, 80)`, deviceID, identityType, value, source); err != nil {
		t.Fatal(err)
	}
}

func findCandidate(items []devices.DuplicateCandidate, a, b string) *devices.DuplicateCandidate {
	for index := range items {
		item := &items[index]
		if (item.DeviceA.ID == a && item.DeviceB.ID == b) || (item.DeviceA.ID == b && item.DeviceB.ID == a) {
			return item
		}
	}
	return nil
}

func TestDuplicateCandidates(t *testing.T) {
	pool := testsupport.MigratedPool(t, "dup_candidates")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	service := devices.NewService(pool)

	// Globally unique MAC on two records: high.
	a := testsupport.CreateDevice(t, pool, "alpha", "00:11:22:33:44:01", "192.0.2.1")
	b := testsupport.CreateDevice(t, pool, "alpha-old", "00:11:22:33:44:99", "192.0.2.2")
	addIdentity(t, pool, b, "mac", "00:11:22:33:44:01", "test")
	// Locally administered (possibly randomized) MAC: medium.
	c := testsupport.CreateDevice(t, pool, "phone", "02:aa:00:00:00:01", "192.0.2.3")
	d := testsupport.CreateDevice(t, pool, "phone-2", "02:aa:00:00:00:77", "192.0.2.4")
	addIdentity(t, pool, d, "mac", "02:aa:00:00:00:01", "test")
	// Only a shared hostname: too weak to report.
	e := testsupport.CreateDevice(t, pool, "printer", "00:11:22:33:44:05", "192.0.2.5")
	f := testsupport.CreateDevice(t, pool, "printer-b", "00:11:22:33:44:06", "192.0.2.6")
	addIdentity(t, pool, f, "hostname", "printer", "test")
	// An enrolled agent reports an address another record holds: medium.
	g := testsupport.CreateDevice(t, pool, "agent-host", "00:11:22:33:44:07", "192.0.2.7")
	h := testsupport.CreateDevice(t, pool, "unknown-at-8", "", "192.0.2.8")
	addIdentity(t, pool, g, "ip", "192.0.2.8", devices.SourceAgent)
	// The same holds when the other record only held the address recently.
	i := testsupport.CreateDevice(t, pool, "unknown-at-9", "", "192.0.2.9")
	if _, err := pool.Exec(ctx, `UPDATE ip_addresses SET valid_until = now() - interval '1 day' WHERE host(address) = '192.0.2.9'`); err != nil {
		t.Fatal(err)
	}
	addIdentity(t, pool, g, "ip", "192.0.2.9", devices.SourceAgent)

	items, err := service.Duplicates(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if candidate := findCandidate(items, a, b); candidate == nil || candidate.Confidence != devices.DuplicateHigh {
		t.Fatalf("shared global MAC: %+v", candidate)
	}
	if candidate := findCandidate(items, c, d); candidate == nil || candidate.Confidence != devices.DuplicateMedium {
		t.Fatalf("shared local MAC: %+v", candidate)
	}
	if candidate := findCandidate(items, e, f); candidate != nil {
		t.Fatalf("hostname-only overlap was reported: %+v", candidate)
	}
	if candidate := findCandidate(items, g, h); candidate == nil || candidate.Confidence != devices.DuplicateMedium {
		t.Fatalf("agent-reported address overlap: %+v", candidate)
	}
	if candidate := findCandidate(items, g, i); candidate == nil || candidate.Confidence != devices.DuplicateMedium {
		t.Fatalf("agent-reported address held recently: %+v", candidate)
	}
	if forDevice, _ := service.Duplicates(ctx, a, 100); len(forDevice) != 1 {
		t.Fatalf("per-device candidates: %d", len(forDevice))
	}
	// Candidates never change anything by themselves.
	if count(t, pool, `SELECT count(*) FROM devices WHERE merged_into IS NOT NULL`) != 0 {
		t.Fatal("duplicate detection merged devices")
	}
	if err := service.DismissDuplicate(ctx, a, strings.ToUpper(a), "", devices.Actor{UserID: adminID}); !errors.Is(err, devices.ErrSameDevice) {
		t.Fatalf("differently cased self-dismissal: %v", err)
	}
	if err := service.DismissDuplicate(ctx, strings.ToUpper(b), a, "two NICs, two hosts", devices.Actor{UserID: adminID}); err != nil {
		t.Fatal(err)
	}
	items, _ = service.Duplicates(ctx, "", 100)
	if findCandidate(items, a, b) != nil {
		t.Fatal("dismissed pair is still a candidate")
	}
}

func TestMergePreservesHistoryAndAgent(t *testing.T) {
	pool := testsupport.MigratedPool(t, "merge_history")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := devices.Actor{UserID: adminID}
	service := devices.NewService(pool)
	agentRepo := testsupport.AgentRepository(pool)

	// The source was discovered and has a management agent; the target is an older record.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.10", Reachable: true, Hostname: "laptop", MAC: "00:11:22:33:55:10", Method: "icmp"})
	source := deviceByMAC(t, pool, "00:11:22:33:55:10")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("laptop", "00:11:22:33:55:10", "192.0.2.10"))
	target := testsupport.CreateDevice(t, pool, "laptop-old", "00:11:22:33:55:11", "192.0.2.11")
	if err := service.UpdateMetadata(ctx, source, devices.MetadataUpdate{Notes: strPtr("bought 2024"), Tags: &[]string{"mobile"}}, actor); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateMetadata(ctx, target, devices.MetadataUpdate{DisplayName: strPtr("Luis laptop"), Tags: &[]string{"finance"}}, actor); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTrust(ctx, target, devices.TrustTrusted, "", actor); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{source, target} {
		if _, err := pool.Exec(ctx, `WITH e AS (INSERT INTO entities (kind, name) VALUES ('service_endpoint', 'ssh') RETURNING id)
			INSERT INTO service_endpoints (entity_id, host_entity_id, protocol, port) SELECT id, $1::uuid, 'tcp', 22 FROM e`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO relationships (source_entity_id, target_entity_id, relationship_type, source, confidence)
		VALUES ($1::uuid, $2::uuid, 'CONNECTED_TO', 'test', 50)`, source, target); err != nil {
		t.Fatal(err)
	}
	sourceObservations := count(t, pool, `SELECT count(*) FROM observations WHERE entity_id = $1::uuid`, source)
	sourceEvents := count(t, pool, `SELECT count(*) FROM events WHERE entity_id = $1::uuid`, source)
	targetEvents := count(t, pool, `SELECT count(*) FROM events WHERE entity_id = $1::uuid`, target)

	// The agent's earlier command stays in the device's history after the merge.
	commands := remote.NewService(pool)
	if err := agentRepo.Heartbeat(ctx, agent.principal, agentHeartbeat(agent.facts, "system.info")); err != nil {
		t.Fatal(err)
	}
	command, err := commands.Create(ctx, remote.CreateInput{AgentID: agent.principal.AgentID, Type: "system.info"}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Merge(ctx, source, source, "", actor); !errors.Is(err, devices.ErrSameDevice) {
		t.Fatalf("self merge: %v", err)
	}
	result, err := service.Merge(ctx, source, target, "same laptop, NIC replaced", actor)
	if err != nil {
		t.Fatal(err)
	}

	// Source is a tombstone that points at the survivor and is hidden from lists.
	inv := inventory.NewRepositories(pool).Devices
	retired, err := inv.Get(ctx, source)
	if err != nil || retired.MergedInto != target {
		t.Fatalf("source tombstone: %+v %v", retired.MergedInto, err)
	}
	params, _ := inventory.NormalizeListParams(inventory.DeviceListParams{})
	if list, _ := inv.List(ctx, params); list.Total != 1 || list.Items[0].ID != target {
		t.Fatalf("merged device still listed: %+v", list.Items)
	}
	survivor, _ := inv.Get(ctx, target)
	if survivor.Name != "Luis laptop" || survivor.Notes != "bought 2024" || survivor.TrustState != devices.TrustUnknown {
		t.Fatalf("survivor metadata: name=%q notes=%q trust=%q", survivor.Name, survivor.Notes, survivor.TrustState)
	}
	if len(survivor.Tags) != 2 {
		t.Fatalf("tags not combined: %v", survivor.Tags)
	}
	// History moved: interfaces, observations, events, services, identities, bindings.
	for name, check := range map[string][2]int{
		"interfaces":   {count(t, pool, `SELECT count(*) FROM network_interfaces WHERE device_id = $1::uuid`, target), 2},
		"observations": {count(t, pool, `SELECT count(*) FROM observations WHERE entity_id = $1::uuid`, target), sourceObservations},
		"events":       {count(t, pool, `SELECT count(*) FROM events WHERE entity_id = $1::uuid`, target), sourceEvents + targetEvents + 1},
		"services":     {count(t, pool, `SELECT count(*) FROM service_endpoints WHERE host_entity_id = $1::uuid`, target), 1},
		"relationship": {count(t, pool, `SELECT count(*) FROM relationships WHERE source_entity_id = $1::uuid OR target_entity_id = $1::uuid`, target), 0},
		"bindings":     {count(t, pool, `SELECT count(*) FROM discovery_bindings WHERE device_id = $1::uuid`, target), 1},
		"source left":  {count(t, pool, `SELECT (SELECT count(*) FROM network_interfaces WHERE device_id = $1::uuid) + (SELECT count(*) FROM device_identities WHERE device_id = $1::uuid) + (SELECT count(*) FROM observations WHERE entity_id = $1::uuid)`, source), 0},
	} {
		if check[0] != check[1] {
			t.Fatalf("%s after merge: got %d, want %d", name, check[0], check[1])
		}
	}
	// The agent kept its association status and moved with the evidence.
	a, _ := agentRepo.Get(ctx, agent.principal.AgentID)
	if a.Device == nil || a.Device.ID != target || a.Association.Status != "automatic" {
		t.Fatalf("agent after merge: %+v", a.Association)
	}
	if count(t, pool, `SELECT count(*) FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'agent'`, target) != 1 {
		t.Fatal("agent identity evidence did not move")
	}
	if history, _ := commands.List(ctx, "", target, 50); len(history) != 1 || history[0].ID != command.ID {
		t.Fatalf("command history lost after merge: %d", len(history))
	}
	// Audit and the append-only merge record.
	if count(t, pool, `SELECT count(*) FROM audit_log WHERE action = 'device.merged' AND target_id = $1 AND safe_parameters->>'sourceDeviceId' = $2`, target, source) != 1 {
		t.Fatal("merge was not audited")
	}
	if _, err := pool.Exec(ctx, `UPDATE device_merges SET reason = 'x' WHERE id = $1::uuid`, result.MergeID); err == nil {
		t.Fatal("merge record is mutable")
	}
	// Nothing can be attached to the retired record any more.
	if _, err := pool.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence) VALUES ($1::uuid, 'hostname', 'late', 'test', 10)`, source); err == nil {
		t.Fatal("evidence was attached to a merged device")
	}
	if _, err := pool.Exec(ctx, `UPDATE devices SET merged_into = NULL, merged_at = NULL WHERE entity_id = $1::uuid`, source); err == nil {
		t.Fatal("merged device was restored")
	}
	if _, err := service.Merge(ctx, source, target, "", actor); !errors.Is(err, devices.ErrMerged) {
		t.Fatalf("merging a retired device again: %v", err)
	}
	// A later discovery observation of the old MAC lands on the survivor.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.12", Reachable: true, MAC: "00:11:22:33:55:10", Method: "icmp"})
	if got, _ := lastMatch(t, pool, "192.0.2.12"); got != target {
		t.Fatalf("observation after merge attributed to %s", got)
	}
}

func agentHeartbeat(facts agentproto.HostFacts, capabilities ...string) agentproto.HeartbeatRequest {
	return agentproto.HeartbeatRequest{Host: facts, Capabilities: capabilities}
}

func TestMergeRefusalsChainsAndConcurrency(t *testing.T) {
	pool := testsupport.MigratedPool(t, "merge_concurrency")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := devices.Actor{UserID: adminID}
	service := devices.NewService(pool)
	agentRepo := testsupport.AgentRepository(pool)

	// Two devices that each have a live agent cannot be merged.
	x := testsupport.CreateDevice(t, pool, "x", "00:11:22:33:66:01", "192.0.2.21")
	y := testsupport.CreateDevice(t, pool, "y", "00:11:22:33:66:02", "192.0.2.22")
	enrollAgent(t, agentRepo, adminID, hostFacts("x", "00:11:22:33:66:01", "192.0.2.21"))
	enrollAgent(t, agentRepo, adminID, hostFacts("y", "00:11:22:33:66:02", "192.0.2.22"))
	if _, err := service.Merge(ctx, x, y, "", actor); !errors.Is(err, devices.ErrAgentConflict) {
		t.Fatalf("two agents: %v", err)
	}

	// Chains: A into B, then B into C leaves A pointing at C.
	a := testsupport.CreateDevice(t, pool, "a", "00:11:22:33:66:11", "")
	b := testsupport.CreateDevice(t, pool, "b", "00:11:22:33:66:12", "")
	c := testsupport.CreateDevice(t, pool, "c", "00:11:22:33:66:13", "")
	if _, err := service.Merge(ctx, a, b, "", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Merge(ctx, c, a, "", actor); !errors.Is(err, devices.ErrMerged) {
		t.Fatalf("merge into a retired device: %v", err)
	}
	if _, err := service.Merge(ctx, b, c, "", actor); err != nil {
		t.Fatal(err)
	}
	var pointer string
	_ = pool.QueryRow(ctx, `SELECT merged_into::text FROM devices WHERE entity_id = $1::uuid`, a).Scan(&pointer)
	if pointer != c {
		t.Fatalf("chained merge pointer = %s, want %s", pointer, c)
	}

	// Opposite concurrent merges of one pair: exactly one succeeds.
	p := testsupport.CreateDevice(t, pool, "p", "00:11:22:33:66:21", "")
	q := testsupport.CreateDevice(t, pool, "q", "00:11:22:33:66:22", "")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for index, pair := range [][2]string{{p, q}, {q, p}} {
		wg.Add(1)
		go func(index int, pair [2]string) {
			defer wg.Done()
			_, errs[index] = service.Merge(ctx, pair[0], pair[1], "", actor)
		}(index, pair)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("concurrent opposite merges: %v / %v", errs[0], errs[1])
	}

	// A discovery-style writer holding its share lock delays the merge; its new
	// evidence then moves with everything else instead of being stranded.
	s := testsupport.CreateDevice(t, pool, "s", "00:11:22:33:66:31", "")
	r := testsupport.CreateDevice(t, pool, "r", "00:11:22:33:66:32", "")
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `SELECT 1 FROM devices WHERE entity_id = $1::uuid FOR SHARE`, s); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Merge(ctx, s, r, "", actor)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("merge did not wait for the writer: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := writer.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence) VALUES ($1::uuid, 'hostname', 'during-merge', 'test', 10)`, s); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count(t, pool, `SELECT count(*) FROM device_identities WHERE value = 'during-merge' AND device_id = $1::uuid`, r) != 1 {
		t.Fatal("evidence written during the merge was stranded on the retired device")
	}
}

func TestSplitRequiresExplicitAgentHandling(t *testing.T) {
	pool := testsupport.MigratedPool(t, "split")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := devices.Actor{UserID: adminID}
	service := devices.NewService(pool)
	agentRepo := testsupport.AgentRepository(pool)

	// Two physical hosts were grouped: two MACs on one record.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.30", Reachable: true, MAC: "00:11:22:33:77:30", Method: "icmp"})
	deviceID := deviceByMAC(t, pool, "00:11:22:33:77:30")
	var secondInterface string
	if err := pool.QueryRow(ctx, `WITH e AS (INSERT INTO entities (kind, name) VALUES ('network_interface', 'eth1') RETURNING id)
		INSERT INTO network_interfaces (entity_id, device_id, name, mac_address) SELECT id, $1::uuid, 'eth1', '00:11:22:33:77:31' FROM e RETURNING entity_id::text`, deviceID).Scan(&secondInterface); err != nil {
		t.Fatal(err)
	}
	addIdentity(t, pool, deviceID, "mac", "00:11:22:33:77:31", "local-discovery")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("other-host", "00:11:22:33:77:31", "192.0.2.31"))
	if _, err := agentRepo.UpdateAssociation(ctx, agent.principal.AgentID, agents.AssociationAction{Action: "confirm", DeviceID: deviceID}, agents.Actor{UserID: adminID}); err != nil {
		t.Fatal(err)
	}

	request := devices.SplitRequest{DisplayName: "Other host", InterfaceIDs: []string{secondInterface}}
	if _, err := service.Split(ctx, deviceID, request, actor); !errors.Is(err, devices.ErrAgentDecisionRequired) {
		t.Fatalf("split without agent decision: %v", err)
	}
	if _, err := service.Split(ctx, deviceID, devices.SplitRequest{}, actor); !errors.Is(err, devices.ErrInvalidInput) {
		t.Fatalf("empty split: %v", err)
	}
	if _, err := service.Split(ctx, deviceID, devices.SplitRequest{InterfaceIDs: []string{"00000000-0000-4000-8000-000000000000"}, Agent: "keep"}, actor); !errors.Is(err, devices.ErrInvalidInput) {
		t.Fatalf("foreign interface: %v", err)
	}
	// An identity ID that is not this device's must be rejected, not silently skipped.
	foreign := testsupport.CreateDevice(t, pool, "foreign", "00:11:22:33:77:99", "")
	if _, err := service.Split(ctx, deviceID, devices.SplitRequest{IdentityIDs: []string{identityID(t, pool, foreign, "mac")}, Agent: "keep"}, actor); !errors.Is(err, devices.ErrInvalidInput) {
		t.Fatalf("foreign identity: %v", err)
	}
	request.Agent = devices.SplitAgentMove
	result, err := service.Split(ctx, deviceID, request, actor)
	if err != nil {
		t.Fatal(err)
	}
	if count(t, pool, `SELECT count(*) FROM network_interfaces WHERE device_id = $1::uuid`, result.NewDeviceID) != 1 ||
		count(t, pool, `SELECT count(*) FROM device_identities WHERE device_id = $1::uuid AND identity_type = 'mac' AND value = '00:11:22:33:77:31' AND source = 'local-discovery'`, result.NewDeviceID) != 1 ||
		count(t, pool, `SELECT count(*) FROM network_interfaces WHERE device_id = $1::uuid`, deviceID) != 1 {
		t.Fatal("split did not move the selected interface and its MAC evidence")
	}
	a, _ := agentRepo.Get(ctx, agent.principal.AgentID)
	if a.Device == nil || a.Device.ID != result.NewDeviceID || a.Association.Status != "confirmed" {
		t.Fatalf("agent after split: %+v", a.Association)
	}
	newDevice, _ := inventory.NewRepositories(pool).Devices.Get(ctx, result.NewDeviceID)
	if newDevice.Name != "Other host" || newDevice.TrustState != devices.TrustUnknown {
		t.Fatalf("new device: %+v", newDevice.DeviceSummary)
	}
	if count(t, pool, `SELECT count(*) FROM audit_log WHERE action = 'device.split' AND target_id = $1`, deviceID) != 1 {
		t.Fatal("split was not audited")
	}

	// Detaching leaves the agent unassociated and removes its evidence from the device.
	if _, err := service.Split(ctx, result.NewDeviceID, devices.SplitRequest{IdentityIDs: []string{identityID(t, pool, result.NewDeviceID, "machine")}, Agent: devices.SplitAgentDetach}, actor); err != nil {
		t.Fatal(err)
	}
	if a, _ := agentRepo.Get(ctx, agent.principal.AgentID); a.Device != nil || a.Association.Status != "unassociated" {
		t.Fatalf("detached agent: %+v", a.Association)
	}
}

func identityID(t *testing.T, pool *pgxpool.Pool, deviceID, identityType string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM device_identities WHERE device_id = $1::uuid AND identity_type = $2 LIMIT 1`, deviceID, identityType).Scan(&id); err != nil {
		t.Fatalf("identity %s: %v", identityType, err)
	}
	return id
}

func TestBlockedDevicesGetNoRemoteActions(t *testing.T) {
	pool := testsupport.MigratedPool(t, "blocked_remote")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	agentRepo := testsupport.AgentRepository(pool)
	deviceID := testsupport.CreateDevice(t, pool, "kiosk", "00:11:22:33:88:01", "192.0.2.40")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("kiosk", "00:11:22:33:88:01", "192.0.2.40"))
	if err := agentRepo.Heartbeat(ctx, agent.principal, agentHeartbeat(agent.facts, "system.info")); err != nil {
		t.Fatal(err)
	}
	commands := remote.NewService(pool)
	if _, err := commands.Create(ctx, remote.CreateInput{AgentID: agent.principal.AgentID, Type: "system.info"}, agents.Actor{UserID: adminID}); err != nil {
		t.Fatalf("command for an unknown-trust device: %v", err)
	}
	if err := devices.NewService(pool).SetTrust(ctx, deviceID, devices.TrustBlocked, "stolen", devices.Actor{UserID: adminID}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Create(ctx, remote.CreateInput{AgentID: agent.principal.AgentID, Type: "system.info"}, agents.Actor{UserID: adminID}); !errors.Is(err, remote.ErrDeviceBlocked) {
		t.Fatalf("command for a blocked device: %v", err)
	}
}
