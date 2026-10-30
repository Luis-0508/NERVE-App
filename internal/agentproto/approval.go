package agentproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// A privileged command reaches an agent only with a CommandApproval: Core's
// signed statement that an operator approved this command, with these
// parameters, for this agent, until ExpiresAt. The agent verifies it against
// the Core key pinned at enrollment before acting, and the privileged helper
// verifies it again, so neither has to trust the claim channel alone.
const (
	approvalSignatureVersion = "NERVE-APPROVAL-V1"
	// DefaultApprovalTTL bounds how long an approval may wait for its claim.
	DefaultApprovalTTL = 10 * time.Minute
	// MaxApprovalTTL is the longest validity Core ever signs.
	MaxApprovalTTL = time.Hour
)

var (
	ErrApprovalMissing  = errors.New("the privileged command carries no approval")
	ErrApprovalInvalid  = errors.New("the approval is not signed by the pinned NERVE Core key")
	ErrApprovalMismatch = errors.New("the approval was issued for a different command")
	ErrApprovalExpired  = errors.New("the approval has expired")
)

type CommandApproval struct {
	CommandID        string    `json:"commandId"`
	AgentID          string    `json:"agentId"`
	DeviceID         string    `json:"deviceId"`
	Type             string    `json:"type"`
	ParametersDigest string    `json:"parametersDigest"`
	ApprovedBy       string    `json:"approvedBy"`
	ApprovedAt       time.Time `json:"approvedAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	KeyID            string    `json:"keyId"`
	Signature        string    `json:"signature"`
}

// ParametersDigest is the hex SHA-256 of the parameters' JSON encoding. Go
// encodes map keys in sorted order, so Core and agent derive the same digest
// from the same decoded parameters.
func ParametersDigest(parameters map[string]any) string {
	if parameters == nil {
		parameters = map[string]any{}
	}
	encoded, _ := json.Marshal(parameters)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// canonical covers every field except the signature. Times are whole Unix
// seconds so a JSON round trip cannot change what was signed.
func (a CommandApproval) canonical() []byte {
	var buffer bytes.Buffer
	for _, field := range []string{approvalSignatureVersion, a.KeyID, a.CommandID, a.AgentID, a.DeviceID, a.Type, a.ParametersDigest,
		a.ApprovedBy, strconv.FormatInt(a.ApprovedAt.Unix(), 10), strconv.FormatInt(a.ExpiresAt.Unix(), 10)} {
		buffer.WriteString(field)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes()
}

// SignCommandApproval fills KeyID and Signature. Times are truncated to whole
// seconds first.
func SignCommandApproval(key ed25519.PrivateKey, approval CommandApproval) CommandApproval {
	approval.ApprovedAt = approval.ApprovedAt.UTC().Truncate(time.Second)
	approval.ExpiresAt = approval.ExpiresAt.UTC().Truncate(time.Second)
	approval.KeyID = CoreKeyID(key.Public().(ed25519.PublicKey))
	approval.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, approval.canonical()))
	return approval
}

// VerifyCommandApproval checks that command carries a genuine, unexpired
// approval issued for exactly this command, agent, type and parameters.
// Replaying an approval on another command fails the binding check; running
// the same command twice is refused by the agent's executed-ID memory.
func VerifyCommandApproval(pinned ed25519.PublicKey, command Command, now time.Time) error {
	approval := command.Approval
	if approval == nil {
		return ErrApprovalMissing
	}
	if len(pinned) != ed25519.PublicKeySize || approval.KeyID != CoreKeyID(pinned) {
		return ErrApprovalInvalid
	}
	signature, err := base64.StdEncoding.DecodeString(approval.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(pinned, approval.canonical(), signature) {
		return ErrApprovalInvalid
	}
	if approval.CommandID != command.ID || approval.AgentID != command.AgentID || approval.Type != command.Type ||
		approval.ParametersDigest != ParametersDigest(command.Parameters) {
		return ErrApprovalMismatch
	}
	if !now.Before(approval.ExpiresAt) || approval.ExpiresAt.Sub(approval.ApprovedAt) > MaxApprovalTTL {
		return ErrApprovalExpired
	}
	return nil
}
