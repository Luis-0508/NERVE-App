package agentproto

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func approvedCommand(t *testing.T, key ed25519.PrivateKey, now time.Time) Command {
	t.Helper()
	command := Command{ID: "11111111-1111-4111-8111-111111111111", AgentID: "22222222-2222-4222-8222-222222222222",
		Type: CapabilitySystemReboot, Parameters: map[string]any{"delaySeconds": 30.0}}
	approval := SignCommandApproval(key, CommandApproval{CommandID: command.ID, AgentID: command.AgentID,
		DeviceID: "33333333-3333-4333-8333-333333333333", Type: command.Type, ParametersDigest: ParametersDigest(command.Parameters),
		ApprovedBy: "44444444-4444-4444-8444-444444444444", ApprovedAt: now, ExpiresAt: now.Add(DefaultApprovalTTL)})
	command.Approval = &approval
	return command
}

func TestCommandApprovalVerifies(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	command := approvedCommand(t, key, now)
	if err := VerifyCommandApproval(key.Public().(ed25519.PublicKey), command, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid approval refused: %v", err)
	}
}

func TestCommandApprovalRefusals(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	pinned := key.Public().(ed25519.PublicKey)
	now := time.Now()

	cases := map[string]struct {
		mutate func(*Command)
		pinned ed25519.PublicKey
		at     time.Time
		want   error
	}{
		"missing": {mutate: func(c *Command) { c.Approval = nil }, want: ErrApprovalMissing},
		"wrong key": {mutate: func(c *Command) {
			signed := SignCommandApproval(otherKey, *c.Approval)
			c.Approval = &signed
		}, want: ErrApprovalInvalid},
		"pinned key differs": {pinned: otherKey.Public().(ed25519.PublicKey), want: ErrApprovalInvalid},
		"tampered digest": {mutate: func(c *Command) {
			changed := *c.Approval
			changed.ParametersDigest = ParametersDigest(map[string]any{"delaySeconds": 0.0})
			c.Approval = &changed
		}, want: ErrApprovalInvalid},
		"tampered parameters": {mutate: func(c *Command) { c.Parameters = map[string]any{"delaySeconds": 0.0} }, want: ErrApprovalMismatch},
		"tampered expiry": {mutate: func(c *Command) {
			changed := *c.Approval
			changed.ExpiresAt = changed.ExpiresAt.Add(time.Hour)
			c.Approval = &changed
		}, want: ErrApprovalInvalid},
		"bad signature encoding": {mutate: func(c *Command) {
			changed := *c.Approval
			changed.Signature = "not base64"
			c.Approval = &changed
		}, want: ErrApprovalInvalid},
		"replayed on another command": {mutate: func(c *Command) { c.ID = "55555555-5555-4555-8555-555555555555" }, want: ErrApprovalMismatch},
		"replayed to another agent":   {mutate: func(c *Command) { c.AgentID = "66666666-6666-4666-8666-666666666666" }, want: ErrApprovalMismatch},
		"different type":              {mutate: func(c *Command) { c.Type = CapabilityCommandShell }, want: ErrApprovalMismatch},
		"expired":                     {at: now.Add(DefaultApprovalTTL + time.Second), want: ErrApprovalExpired},
		"too long validity": {mutate: func(c *Command) {
			signed := SignCommandApproval(key, CommandApproval{CommandID: c.ID, AgentID: c.AgentID, Type: c.Type,
				ParametersDigest: ParametersDigest(c.Parameters), ApprovedAt: now, ExpiresAt: now.Add(2 * MaxApprovalTTL)})
			c.Approval = &signed
		}, want: ErrApprovalExpired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			command := approvedCommand(t, key, now)
			if tc.mutate != nil {
				tc.mutate(&command)
			}
			verifyKey := pinned
			if tc.pinned != nil {
				verifyKey = tc.pinned
			}
			at := now.Add(time.Minute)
			if !tc.at.IsZero() {
				at = tc.at
			}
			if err := VerifyCommandApproval(verifyKey, command, at); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParametersDigestIsOrderIndependent(t *testing.T) {
	a := ParametersDigest(map[string]any{"a": 1.0, "b": "x"})
	b := ParametersDigest(map[string]any{"b": "x", "a": 1.0})
	if a != b || ParametersDigest(nil) != ParametersDigest(map[string]any{}) {
		t.Fatal("digest depends on map order or nil-ness")
	}
}
