package agentproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const rotationProofVersion = "NERVE-AGENT-ROTATE-V1"

// ErrInvalidRotationProof is returned when the new key's proof of possession
// does not verify or is outside the accepted clock skew.
var ErrInvalidRotationProof = errors.New("credential rotation proof is invalid")

// RotateCredentialRequest replaces the agent's key. The request itself is
// signed with the current credential; Proof is the new key's signature over
// RotationProofCanonical, so Core knows the agent holds the new private key.
// The agent chooses the new credential ID, which lets it find out after a
// crash whether Core committed the rotation.
type RotateCredentialRequest struct {
	CredentialID string `json:"credentialId"`
	PublicKey    string `json:"publicKey"`
	Timestamp    int64  `json:"timestamp"`
	Proof        string `json:"proof"`
}

type RotateCredentialResponse struct {
	CredentialID string    `json:"credentialId"`
	Fingerprint  string    `json:"fingerprint"`
	CreatedAt    time.Time `json:"createdAt"`
}

// RotationProofCanonical binds the proof to the agent, the credential being
// replaced, the proposed credential and key, and a timestamp.
func RotationProofCanonical(agentID, oldCredentialID, newCredentialID string, publicKey ed25519.PublicKey, timestamp int64) []byte {
	var buffer bytes.Buffer
	for _, part := range []string{rotationProofVersion, agentID, oldCredentialID, newCredentialID,
		EncodePublicKey(publicKey), strconv.FormatInt(timestamp, 10)} {
		buffer.WriteString(part)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

// SignRotation builds a rotation request for the new key.
func SignRotation(agentID, oldCredentialID, newCredentialID string, key ed25519.PrivateKey, now time.Time) RotateCredentialRequest {
	public := key.Public().(ed25519.PublicKey)
	timestamp := now.Unix()
	proof := ed25519.Sign(key, RotationProofCanonical(agentID, oldCredentialID, newCredentialID, public, timestamp))
	return RotateCredentialRequest{CredentialID: newCredentialID, PublicKey: EncodePublicKey(public), Timestamp: timestamp,
		Proof: base64.StdEncoding.EncodeToString(proof)}
}

// VerifyRotation checks the proof of possession and returns the new key.
func VerifyRotation(request RotateCredentialRequest, agentID, oldCredentialID string, now time.Time) (ed25519.PublicKey, error) {
	if !uuidPattern.MatchString(request.CredentialID) {
		return nil, ErrInvalidRotationProof
	}
	publicKey, err := DecodePublicKey(request.PublicKey)
	if err != nil {
		return nil, ErrInvalidRotationProof
	}
	if now.Sub(time.Unix(request.Timestamp, 0)).Abs() > MaxClockSkew {
		return nil, ErrInvalidRotationProof
	}
	proof, err := base64.StdEncoding.DecodeString(strings.TrimSpace(request.Proof))
	if err != nil || len(proof) != ed25519.SignatureSize ||
		!ed25519.Verify(publicKey, RotationProofCanonical(agentID, oldCredentialID, request.CredentialID, publicKey, request.Timestamp), proof) {
		return nil, ErrInvalidRotationProof
	}
	return publicKey, nil
}
