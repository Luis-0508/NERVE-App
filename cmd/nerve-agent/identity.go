package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const identityFileName = "identity.json"

// identity is the agent's enrolled credential. The Ed25519 private key never
// leaves this file; Core only knows the public key.
type identity struct {
	Version      int       `json:"version"`
	AgentID      string    `json:"agentId"`
	CredentialID string    `json:"credentialId"`
	Fingerprint  string    `json:"fingerprint"`
	APIURL       string    `json:"apiUrl"`
	PrivateKey   string    `json:"privateKey"`
	CoreKey      string    `json:"coreKey"`
	EnrolledAt   time.Time `json:"enrolledAt"`
	// CredentialCreatedAt is when the current key was registered; identities
	// written before key rotation existed fall back to EnrolledAt.
	CredentialCreatedAt time.Time `json:"credentialCreatedAt,omitzero"`

	key     ed25519.PrivateKey
	coreKey ed25519.PublicKey
	// Set only right after enrollment, for the operator-facing messages.
	deploymentKey, pendingApproval bool
}

// credentialAge is how long the current key has been in use.
func (id *identity) credentialAge(now time.Time) time.Duration {
	created := id.CredentialCreatedAt
	if created.IsZero() {
		created = id.EnrolledAt
	}
	return now.Sub(created)
}

// tokenNotice tells the operator what happens to the token they just used.
func (id *identity) tokenNotice() string {
	if id.deploymentKey {
		return "The deployment key stays valid for further endpoints until it expires, runs out of uses or is revoked."
	}
	return "The enrollment token is now used up; remove it from any configuration and delete any token file."
}

// approvalNotice is empty unless an operator still has to approve the agent.
func (id *identity) approvalNotice() string {
	if !id.pendingApproval {
		return ""
	}
	return "This agent waits for approval in NERVE (Settings → Management agents) and receives no commands until then."
}

// defaultStateDir picks a per-installation location that only the agent's
// account (and administrators) can read. NERVE_AGENT_STATE_DIR overrides it.
// When the agent is installed as a service, administrators get the machine
// directory automatically, so `status` and `enroll --force` act on the
// service's identity.
func defaultStateDir() string {
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_STATE_DIR")); value != "" {
		return value
	}
	machine := machineStateDir()
	if _, err := os.Stat(filepath.Join(machine, configFileName)); err == nil {
		return machine
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "NERVE", "agent")
		}
	}
	if os.Geteuid() == 0 {
		return machine
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "nerve-agent")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "nerve-agent")
	}
	return "nerve-agent-state"
}

// machineStateDir is the state directory of an installed service.
func machineStateDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "NERVE", "agent")
	}
	return "/var/lib/nerve-agent"
}

func loadIdentity(dir string) (*identity, error) {
	path := filepath.Join(dir, identityFileName)
	if err := checkPrivateFile(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value identity
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse agent identity: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(value.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize || value.Version != 1 || value.AgentID == "" || value.CredentialID == "" {
		return nil, errors.New("agent identity file is invalid; re-enroll the agent")
	}
	coreKey, err := base64.StdEncoding.DecodeString(value.CoreKey)
	if err != nil || len(coreKey) != ed25519.PublicKeySize {
		return nil, errors.New("agent identity has no pinned NERVE Core key; re-enroll the agent")
	}
	value.coreKey = coreKey
	value.key = ed25519.NewKeyFromSeed(seed)
	return &value, nil
}

// saveIdentity writes the identity atomically into a directory restricted to
// the agent's account.
func saveIdentity(dir string, value *identity) error {
	value.Version = 1
	value.PrivateKey = base64.StdEncoding.EncodeToString(value.key.Seed())
	value.CoreKey = base64.StdEncoding.EncodeToString(value.coreKey)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(dir, identityFileName, encoded)
}
