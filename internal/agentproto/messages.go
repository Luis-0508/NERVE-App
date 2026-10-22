package agentproto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// EnrollmentTokenPrefix identifies NERVE enrollment tokens in configuration and
// secret scanners. Format:
//
//	nerve-enroll-v2.<enrollment UUID>.<base64url secret>.<base64url SHA-256 of the Core public key>
//
// The Core key fingerprint travels out of band with the token, so the agent
// can authenticate Core even when enrollment itself runs over plain HTTP.
const EnrollmentTokenPrefix = "nerve-enroll-v2"

// DeploymentKeyPrefix identifies reusable deployment keys. They have the same
// shape as enrollment tokens, with the deployment key UUID in place of the
// enrollment UUID, and are accepted wherever an enrollment token is.
const DeploymentKeyPrefix = "nerve-deploy-v1"

var (
	ErrInvalidEnrollmentToken = errors.New("enrollment token is malformed")
	uuidPattern               = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// NewEnrollmentSecret returns 32 random bytes encoded for a token.
func NewEnrollmentSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate enrollment secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// EnrollmentToken is a parsed enrollment token or deployment key.
type EnrollmentToken struct {
	ID              string
	Secret          []byte
	CoreFingerprint string
	// Deployment is true for a reusable deployment key; ID is then the key's ID.
	Deployment bool
}

func FormatEnrollmentToken(enrollmentID, secret, coreFingerprint string) string {
	return EnrollmentTokenPrefix + "." + enrollmentID + "." + secret + "." + coreFingerprint
}

func FormatDeploymentKey(keyID, secret, coreFingerprint string) string {
	return DeploymentKeyPrefix + "." + keyID + "." + secret + "." + coreFingerprint
}

// ParseEnrollmentToken splits and validates an enrollment token or deployment key.
func ParseEnrollmentToken(token string) (EnrollmentToken, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 4 || (parts[0] != EnrollmentTokenPrefix && parts[0] != DeploymentKeyPrefix) || !uuidPattern.MatchString(parts[1]) {
		return EnrollmentToken{}, ErrInvalidEnrollmentToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return EnrollmentToken{}, ErrInvalidEnrollmentToken
	}
	if fingerprint, err := base64.RawURLEncoding.DecodeString(parts[3]); err != nil || len(fingerprint) != 32 {
		return EnrollmentToken{}, ErrInvalidEnrollmentToken
	}
	return EnrollmentToken{ID: parts[1], Secret: secret, CoreFingerprint: parts[3], Deployment: parts[0] == DeploymentKeyPrefix}, nil
}

// HostFacts are self-reported by the agent. Core treats them as evidence, not
// as proof: they help propose a device association but never override an
// operator decision.
type HostFacts struct {
	Hostname           string          `json:"hostname"`
	OS                 string          `json:"os"`
	OSVersion          string          `json:"osVersion,omitempty"`
	Architecture       string          `json:"architecture"`
	AgentVersion       string          `json:"agentVersion"`
	MachineFingerprint string          `json:"machineFingerprint,omitempty"`
	Interfaces         []InterfaceFact `json:"interfaces"`
}

type InterfaceFact struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses"`
}

const (
	MaxInterfaces         = 64
	MaxAddressesPerIface  = 32
	MaxFactLength         = 256
	MaxCapabilities       = 64
	MaxLastErrorLength    = 1000
	MaxEnrollRequestBytes = 64 << 10
)

// Validate bounds untrusted host facts before they reach the database.
func (f HostFacts) Validate() error {
	for _, value := range []string{f.Hostname, f.OS, f.OSVersion, f.Architecture, f.AgentVersion, f.MachineFingerprint} {
		if len(value) > MaxFactLength {
			return errors.New("host facts contain an oversized value")
		}
	}
	if strings.TrimSuffix(strings.TrimSpace(f.Hostname), ".") == "" || strings.TrimSpace(f.OS) == "" {
		return errors.New("host facts require hostname and os")
	}
	for _, value := range []string{f.Hostname, f.OS, f.OSVersion, f.Architecture, f.AgentVersion, f.MachineFingerprint} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("host facts must be valid UTF-8 without NUL characters")
		}
	}
	if len(f.Interfaces) > MaxInterfaces {
		return errors.New("host facts contain too many interfaces")
	}
	for _, iface := range f.Interfaces {
		if len(iface.Name) > MaxFactLength || len(iface.MAC) > 64 || len(iface.Addresses) > MaxAddressesPerIface {
			return errors.New("host facts contain an oversized interface")
		}
	}
	return nil
}

// EnrollRequest registers a newly generated Ed25519 public key. The request is
// signed with the matching private key (credential header "enroll").
type EnrollRequest struct {
	Token     string    `json:"token"`
	PublicKey string    `json:"publicKey"`
	Host      HostFacts `json:"host"`
}

type EnrollResponse struct {
	AgentID                  string `json:"agentId"`
	CredentialID             string `json:"credentialId"`
	Fingerprint              string `json:"fingerprint"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
	// PendingApproval is set when a deployment key enrolled the agent and an
	// operator still has to approve it. Until then it receives no commands.
	PendingApproval bool `json:"pendingApproval,omitempty"`
}

// IdentityResponse answers an authenticated identity check.
type IdentityResponse struct {
	AgentID      string `json:"agentId"`
	CredentialID string `json:"credentialId"`
	Status       string `json:"status"`
}

// ErrorResponse mirrors the API error envelope.
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Error codes returned to agents. They are stable so the agent can decide
// whether to retry, back off or require re-enrollment.
const (
	CodeAuthenticationFailed = "agent_authentication_failed"
	CodeCredentialRevoked    = "agent_credential_revoked"
	CodeClockSkew            = "agent_clock_skew"
	CodeReplay               = "agent_request_replayed"
	CodeEnrollmentRejected   = "enrollment_rejected"
)
