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
// secret scanners. Format: nerve-enroll-v1.<enrollment UUID>.<base64url secret>.
const EnrollmentTokenPrefix = "nerve-enroll-v1"

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

func FormatEnrollmentToken(enrollmentID, secret string) string {
	return EnrollmentTokenPrefix + "." + enrollmentID + "." + secret
}

// ParseEnrollmentToken splits a token into its enrollment ID and decoded secret.
func ParseEnrollmentToken(token string) (string, []byte, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != EnrollmentTokenPrefix || !uuidPattern.MatchString(parts[1]) {
		return "", nil, ErrInvalidEnrollmentToken
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return "", nil, ErrInvalidEnrollmentToken
	}
	return parts[1], secret, nil
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
