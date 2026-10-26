package agentproto

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestVerifyRotation(t *testing.T) {
	const (
		agentID = "11111111-1111-4111-8111-111111111111"
		oldID   = "22222222-2222-4222-8222-222222222222"
		newID   = "33333333-3333-4333-8333-333333333333"
	)
	_, key, _ := ed25519.GenerateKey(nil)
	_, otherKey, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	valid := SignRotation(agentID, oldID, newID, key, now)

	publicKey, err := VerifyRotation(valid, agentID, oldID, now.Add(time.Minute))
	if err != nil || !publicKey.Equal(key.Public()) {
		t.Fatalf("valid proof rejected: %v", err)
	}

	tampered := func(change func(*RotateCredentialRequest)) RotateCredentialRequest {
		request := valid
		change(&request)
		return request
	}
	cases := map[string]struct {
		request          RotateCredentialRequest
		agentID, oldID   string
		now              time.Time
	}{
		"other agent":          {valid, "44444444-4444-4444-8444-444444444444", oldID, now},
		"other old credential": {valid, agentID, "55555555-5555-4555-8555-555555555555", now},
		"stale":                {valid, agentID, oldID, now.Add(MaxClockSkew + time.Second)},
		"other credential id": {tampered(func(r *RotateCredentialRequest) { r.CredentialID = "66666666-6666-4666-8666-666666666666" }), agentID, oldID, now},
		"other key": {tampered(func(r *RotateCredentialRequest) {
			r.PublicKey = EncodePublicKey(otherKey.Public().(ed25519.PublicKey))
		}), agentID, oldID, now},
		"shifted timestamp":   {tampered(func(r *RotateCredentialRequest) { r.Timestamp++ }), agentID, oldID, now},
		"malformed id":        {tampered(func(r *RotateCredentialRequest) { r.CredentialID = "not-a-uuid" }), agentID, oldID, now},
		"malformed proof":     {tampered(func(r *RotateCredentialRequest) { r.Proof = "AAAA" }), agentID, oldID, now},
		"signed by other key": {signRotationWithKey(agentID, oldID, newID, key, otherKey, now), agentID, oldID, now},
	}
	for name, test := range cases {
		if _, err := VerifyRotation(test.request, test.agentID, test.oldID, test.now); err != ErrInvalidRotationProof {
			t.Errorf("%s: got %v, want ErrInvalidRotationProof", name, err)
		}
	}
}

// signRotationWithKey announces one key but signs with another.
func signRotationWithKey(agentID, oldID, newID string, announced, signer ed25519.PrivateKey, now time.Time) RotateCredentialRequest {
	request := SignRotation(agentID, oldID, newID, signer, now)
	request.PublicKey = EncodePublicKey(announced.Public().(ed25519.PublicKey))
	return request
}
