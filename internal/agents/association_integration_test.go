//go:build integration

package agents_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func hostFacts(hostname, mac, address string) agentproto.HostFacts {
	return agentproto.HostFacts{Hostname: hostname, OS: "linux", Architecture: "amd64", AgentVersion: "test",
		Interfaces: []agentproto.InterfaceFact{{Name: "eth0", MAC: mac, Addresses: []string{address}}}}
}

func TestHeartbeatAndDeviceAssociation(t *testing.T) {
	pool := testsupport.MigratedPool(t, "agents_hb")
	repo := agents.NewRepository(pool)
	ctx := context.Background()
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	actor := agents.Actor{UserID: adminID}

	nas := testsupport.CreateDevice(t, pool, "nas", "02:00:00:00:00:10", "192.0.2.10")
	printer := testsupport.CreateDevice(t, pool, "printer", "02:00:00:00:00:20", "192.0.2.20")

	newAgent := func(facts agentproto.HostFacts) agents.Principal {
		t.Helper()
		_, token, err := repo.CreateEnrollment(ctx, agents.CreateEnrollmentInput{}, actor)
		if err != nil {
			t.Fatal(err)
		}
		public, _, _ := ed25519.GenerateKey(nil)
		result, err := repo.Enroll(ctx, token, public, facts, "")
		if err != nil {
			t.Fatal(err)
		}
		return agents.Principal{AgentID: result.AgentID, CredentialID: result.CredentialID}
	}
	get := func(id string) agents.Agent {
		t.Helper()
		agent, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}

	// A unique MAC match associates automatically at enrollment.
	a := newAgent(hostFacts("nas", "02:00:00:00:00:10", "192.0.2.10"))
	if agent := get(a.AgentID); agent.Association.Status != "automatic" || agent.Device == nil || agent.Device.ID != nas {
		t.Fatalf("unique MAC did not associate: %+v", agent.Association)
	}

	// Hostname/IP-only evidence stays a candidate.
	b := newAgent(hostFacts("printer", "02:00:00:00:00:99", "192.0.2.20"))
	agentB := get(b.AgentID)
	if agentB.Association.Status != "candidate" || agentB.Device != nil || len(agentB.Association.Evidence) != 1 ||
		agentB.Association.Evidence[0].DeviceID != printer || agentB.Association.Evidence[0].Strength != "weak" {
		t.Fatalf("weak evidence was not kept as a candidate: %+v", agentB.Association)
	}

	// Heartbeats store only known capabilities and re-evaluate changed facts.
	err := repo.Heartbeat(ctx, b, agentproto.HeartbeatRequest{Host: hostFacts("printer", "02:00:00:00:00:20", "192.0.2.20"),
		Capabilities: []string{"system.info", "made.up", "system.info", "process.list"}})
	if err != nil {
		t.Fatal(err)
	}
	agentB = get(b.AgentID)
	if len(agentB.Capabilities) != 2 || agentB.Capabilities[0] != "system.info" || agentB.Capabilities[1] != "process.list" {
		t.Fatalf("capabilities not filtered: %v", agentB.Capabilities)
	}
	if agentB.LastHeartbeatAt == nil || agentB.Association.Status != "automatic" || agentB.Device.ID != printer {
		t.Fatalf("heartbeat did not re-evaluate association: %+v", agentB.Association)
	}

	// Operator decisions are sticky: a dismissed agent is not re-associated by heartbeats.
	if _, err := repo.UpdateAssociation(ctx, b.AgentID, agents.AssociationAction{Action: "dismiss"}, actor); err != nil {
		t.Fatal(err)
	}
	if err := repo.Heartbeat(ctx, b, agentproto.HeartbeatRequest{Host: hostFacts("printer2", "02:00:00:00:00:20", "192.0.2.20")}); err != nil {
		t.Fatal(err)
	}
	if agent := get(b.AgentID); agent.Association.Status != "dismissed" || agent.Device != nil {
		t.Fatalf("heartbeat overrode a dismissed association: %+v", agent.Association)
	}
	if agent, err := repo.UpdateAssociation(ctx, b.AgentID, agents.AssociationAction{Action: "reevaluate"}, actor); err != nil || agent.Association.Status != "automatic" {
		t.Fatalf("re-evaluation: %+v %v", agent.Association, err)
	}

	// Confirming a device that already has a live agent is refused.
	if _, err := repo.UpdateAssociation(ctx, b.AgentID, agents.AssociationAction{Action: "confirm", DeviceID: nas}, actor); !errors.Is(err, agents.ErrDeviceHasAgent) {
		t.Fatalf("double association: got %v", err)
	}
	if agent, err := repo.UpdateAssociation(ctx, b.AgentID, agents.AssociationAction{Action: "confirm", DeviceID: printer}, actor); err != nil || agent.Association.Status != "confirmed" {
		t.Fatalf("confirm: %+v %v", agent.Association, err)
	}
	// Confirmed associations survive changed evidence.
	if err := repo.Heartbeat(ctx, b, agentproto.HeartbeatRequest{Host: hostFacts("elsewhere", "02:00:00:00:00:10", "192.0.2.99")}); err != nil {
		t.Fatal(err)
	}
	if agent := get(b.AgentID); agent.Association.Status != "confirmed" || agent.Device.ID != printer {
		t.Fatalf("heartbeat overrode a confirmed association: %+v", agent.Association)
	}

	// A MAC shared by two devices is ambiguous and never auto-associates.
	_, _ = pool.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence) VALUES ($1::uuid, 'mac', '02:00:00:00:00:77', 'test', 90), ($2::uuid, 'mac', '02:00:00:00:00:77', 'test', 90)`, nas, printer)
	c := newAgent(hostFacts("mystery", "02:00:00:00:00:77", "198.51.100.1"))
	if agent := get(c.AgentID); agent.Association.Status != "candidate" || agent.Device != nil || len(agent.Association.Evidence) != 2 {
		t.Fatalf("ambiguous MAC associated: %+v", agent.Association)
	}

	// A device with a live agent turns a second MAC match into a candidate with a note.
	d := newAgent(hostFacts("nas-reinstalled", "02:00:00:00:00:10", "192.0.2.10"))
	agentD := get(d.AgentID)
	if agentD.Association.Status != "candidate" || agentD.Association.Evidence[0].Note == "" {
		t.Fatalf("second agent for a device was associated: %+v", agentD.Association)
	}

	for _, action := range []string{"agent.association.changed", "agent.association.evaluated"} {
		if auditCount(t, pool, action, "success") == 0 {
			t.Fatalf("missing audit %s", action)
		}
	}
	if _, err := repo.UpdateAssociation(ctx, b.AgentID, agents.AssociationAction{Action: "nonsense"}, actor); !errors.Is(err, agents.ErrInvalidInput) {
		t.Fatalf("invalid action: %v", err)
	}

	// A device discovered after enrollment is linked on the next heartbeat, even with unchanged facts.
	lateFacts := hostFacts("late-host", "02:00:00:00:00:55", "203.0.113.55")
	late := newAgent(lateFacts)
	if agent := get(late.AgentID); agent.Association.Status != "unassociated" {
		t.Fatalf("late agent unexpectedly associated: %+v", agent.Association)
	}
	lateDevice := testsupport.CreateDevice(t, pool, "discovered-later", "02:00:00:00:00:55", "")
	if err := repo.Heartbeat(ctx, late, agentproto.HeartbeatRequest{Host: lateFacts}); err != nil {
		t.Fatal(err)
	}
	if agent := get(late.AgentID); agent.Association.Status != "automatic" || agent.Device == nil || agent.Device.ID != lateDevice {
		t.Fatalf("late device was not associated: %+v", agent.Association)
	}
}
