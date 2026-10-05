//go:build integration

package devices_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func countDevices(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM devices WHERE merged_into IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func lastMatch(t *testing.T, pool *pgxpool.Pool, address string) (string, string) {
	t.Helper()
	var deviceID, match string
	if err := pool.QueryRow(context.Background(), `SELECT entity_id::text, evidence->>'match' FROM observations
		WHERE evidence->>'address' = $1 ORDER BY observed_at DESC, id DESC LIMIT 1`, address).Scan(&deviceID, &match); err != nil {
		t.Fatalf("observation for %s: %v", address, err)
	}
	return deviceID, match
}

type enrolledAgent struct {
	principal agents.Principal
	facts     agentproto.HostFacts
}

func enrollAgent(t *testing.T, repo *agents.Repository, adminID string, facts agentproto.HostFacts) enrolledAgent {
	t.Helper()
	ctx := context.Background()
	_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, agents.Actor{UserID: adminID})
	if err != nil {
		t.Fatal(err)
	}
	public, _, _ := ed25519.GenerateKey(nil)
	result, err := repo.Enroll(ctx, token, public, facts, "")
	if err != nil {
		t.Fatal(err)
	}
	return enrolledAgent{principal: agents.Principal{AgentID: result.AgentID, CredentialID: result.CredentialID}, facts: facts}
}

func hostFacts(hostname, mac string, addresses ...string) agentproto.HostFacts {
	return agentproto.HostFacts{Hostname: hostname, OS: "windows", OSVersion: "Windows 11 Pro", Architecture: "amd64", AgentVersion: "test",
		MachineFingerprint: "a3f1c2d4e5b6a7980a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f6071",
		Interfaces:         []agentproto.InterfaceFact{{Name: "Ethernet", MAC: mac, Addresses: addresses}}}
}

func TestAgentDeviceSurvivesAddressChanges(t *testing.T) {
	pool := testsupport.MigratedPool(t, "identity_ipchange")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	agentRepo := testsupport.AgentRepository(pool)

	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.10", Reachable: true, Hostname: "desktop-abc", MAC: "02:00:00:00:00:10", Method: "icmp"})
	deviceID := deviceByMAC(t, pool, "02:00:00:00:00:10")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("Luis-PC", "02:00:00:00:00:10", "192.0.2.10"))
	if a, _ := agentRepo.Get(ctx, agent.principal.AgentID); a.Device == nil || a.Device.ID != deviceID || a.Association.Status != "automatic" {
		t.Fatalf("agent not associated with the discovered device: %+v", a.Association)
	}

	// The host moves to a new address (DHCP) and reports it.
	moved := hostFacts("Luis-PC", "02:00:00:00:00:10", "192.0.2.77", "2001:db8::77")
	if err := agentRepo.Heartbeat(ctx, agent.principal, agentproto.HeartbeatRequest{Host: moved}); err != nil {
		t.Fatal(err)
	}
	// Discovery sees the new IPv4 with the MAC, and the IPv6 address without one.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.77", Reachable: true, MAC: "02:00:00:00:00:10", Method: "icmp"})
	if got, match := lastMatch(t, pool, "192.0.2.77"); got != deviceID || match != "mac" {
		t.Fatalf("new IPv4 attributed to %s via %s", got, match)
	}
	if countDevices(t, pool) != 1 {
		t.Fatal("an IP change created another device")
	}
	if a, _ := agentRepo.Get(ctx, agent.principal.AgentID); a.Device == nil || a.Device.ID != deviceID {
		t.Fatal("agent association lost after the IP change")
	}

	// Agent facts are evidence on the device and outrank reverse DNS.
	report, err := devices.Identity(ctx, pool, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, item := range report.Evidence {
		if _, seen := types[item.Type]; !seen {
			types[item.Type] = item.Strength
		}
	}
	if types["agent"] != devices.StrengthStrong || types["machine"] != devices.StrengthStrong || types["ip"] != devices.StrengthWeak ||
		report.Strength != devices.StrengthStrong || len(report.Agents) != 1 {
		t.Fatalf("identity report: %v strength=%s agents=%d", types, report.Strength, len(report.Agents))
	}
	var hostname, operatingSystem string
	_ = pool.QueryRow(ctx, `SELECT hostname, operating_system FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(&hostname, &operatingSystem)
	if hostname != "Luis-PC" || operatingSystem != "Windows 11 Pro" {
		t.Fatalf("agent facts not effective: %q %q", hostname, operatingSystem)
	}

	// Removing the agent takes its evidence with it; reverse DNS becomes effective again.
	if _, err := agentRepo.Remove(ctx, agent.principal.AgentID, agents.Actor{UserID: adminID}); err != nil {
		t.Fatal(err)
	}
	report, _ = devices.Identity(ctx, pool, deviceID)
	for _, item := range report.Evidence {
		if item.Source == devices.SourceAgent {
			t.Fatalf("agent evidence survived removal: %+v", item)
		}
	}
	_ = pool.QueryRow(ctx, `SELECT hostname, operating_system IS NULL FROM devices WHERE entity_id = $1::uuid`, deviceID).Scan(&hostname, new(bool))
	if hostname != "desktop-abc" {
		t.Fatalf("hostname did not fall back to reverse DNS: %q", hostname)
	}
}

func TestAgentReportedAddressPreventsDuplicateWithoutMAC(t *testing.T) {
	pool := testsupport.MigratedPool(t, "identity_agentip")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	agentRepo := testsupport.AgentRepository(pool)
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.20", Reachable: true, MAC: "02:00:00:00:00:20", Method: "icmp"})
	deviceID := deviceByMAC(t, pool, "02:00:00:00:00:20")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("nas", "02:00:00:00:00:20", "192.0.2.20", "192.0.2.21"))
	_ = agent
	// 192.0.2.21 is seen without a MAC (for example across a router): it belongs to the agent's device.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.21", Reachable: true, Method: "tcp:443"})
	if got, match := lastMatch(t, pool, "192.0.2.21"); got != deviceID || match != "agent-reported-address" {
		t.Fatalf("agent-reported address attributed to %s via %s", got, match)
	}
	if countDevices(t, pool) != 1 {
		t.Fatal("agent-reported address created a duplicate")
	}
	_ = ctx
}

func TestAmbiguousAndReusedEvidenceNeverMerges(t *testing.T) {
	pool := testsupport.MigratedPool(t, "identity_ambiguous")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")

	// Two existing records claim the same MAC (for example a cloned VM).
	a := testsupport.CreateDevice(t, pool, "clone-a", "02:00:00:00:00:30", "192.0.2.30")
	b := testsupport.CreateDevice(t, pool, "clone-b", "02:00:00:00:00:30", "192.0.2.31")
	_, _ = pool.Exec(ctx, `UPDATE entities SET last_seen_at = now() + interval '1 minute' WHERE id = $1::uuid`, b)
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.32", Reachable: true, MAC: "02:00:00:00:00:30", Method: "icmp"})
	got, match := lastMatch(t, pool, "192.0.2.32")
	if got != b || match != "mac-ambiguous" {
		t.Fatalf("ambiguous MAC attributed to %s via %s", got, match)
	}
	if countDevices(t, pool) != 2 {
		t.Fatal("ambiguous evidence changed the number of devices")
	}
	report, _ := devices.Identity(ctx, pool, a)
	shared := false
	for _, item := range report.Evidence {
		if item.Type == "mac" && len(item.SharedWith) == 1 && item.SharedWith[0].ID == b && item.Strength == devices.StrengthModerate {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("shared MAC not exposed as conflicting evidence: %+v", report.Evidence)
	}

	// Address reuse: a known device's address now answers with a different, unknown MAC.
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.40", Reachable: true, MAC: "02:00:00:00:00:40", Method: "icmp"})
	original := deviceByMAC(t, pool, "02:00:00:00:00:40")
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.40", Reachable: true, MAC: "02:00:00:00:00:41", Method: "icmp"})
	replacement := deviceByMAC(t, pool, "02:00:00:00:00:41")
	if replacement == original {
		t.Fatal("new hardware on a reused address was attached to the old device")
	}
	if _, match := lastMatch(t, pool, "192.0.2.40"); match != "address-reused-by-other-hardware" {
		t.Fatalf("reuse not recorded: %s", match)
	}
	var current int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM network_interfaces ni JOIN interface_addresses ia ON ia.interface_id = ni.entity_id
		JOIN ip_addresses ip ON ip.entity_id = ia.ip_address_id WHERE ni.device_id = $1::uuid AND ip.valid_until IS NULL`, original).Scan(&current)
	if current != 0 {
		t.Fatal("the old device still holds the reused address as current")
	}
}

func TestApprovedAssociationIsStable(t *testing.T) {
	pool := testsupport.MigratedPool(t, "identity_confirmed")
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	agentRepo := testsupport.AgentRepository(pool)
	target := testsupport.CreateDevice(t, pool, "server", "02:00:00:00:00:50", "192.0.2.50")
	other := testsupport.CreateDevice(t, pool, "other", "02:00:00:00:00:51", "192.0.2.51")
	agent := enrollAgent(t, agentRepo, adminID, hostFacts("server", "02:00:00:00:00:99", "192.0.2.50"))
	if _, err := agentRepo.UpdateAssociation(ctx, agent.principal.AgentID, agents.AssociationAction{Action: "confirm", DeviceID: target}, agents.Actor{UserID: adminID}); err != nil {
		t.Fatal(err)
	}
	// The agent now reports the other device's MAC and address; discovery runs too.
	if err := agentRepo.Heartbeat(ctx, agent.principal, agentproto.HeartbeatRequest{Host: hostFacts("server", "02:00:00:00:00:51", "192.0.2.51")}); err != nil {
		t.Fatal(err)
	}
	scan(t, pool, adminID, discovery.Observation{Address: "192.0.2.51", Reachable: true, MAC: "02:00:00:00:00:51", Method: "icmp"})
	a, _ := agentRepo.Get(ctx, agent.principal.AgentID)
	if a.Association.Status != "confirmed" || a.Device == nil || a.Device.ID != target {
		t.Fatalf("approved association changed: %+v", a.Association)
	}
	_ = other
}
