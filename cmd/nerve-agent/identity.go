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

	key     ed25519.PrivateKey
	coreKey ed25519.PublicKey
}

// defaultStateDir picks a per-installation location that only the agent's
// account (and administrators) can read. NERVE_AGENT_STATE_DIR overrides it.
func defaultStateDir() string {
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_STATE_DIR")); value != "" {
		return value
	}
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, "NERVE", "agent")
		}
	}
	if os.Geteuid() == 0 {
		return "/var/lib/nerve-agent"
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "nerve-agent")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "nerve-agent")
	}
	return "nerve-agent-state"
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent state directory: %w", err)
	}
	if err := securePrivateDir(dir); err != nil {
		return fmt.Errorf("restrict agent state directory: %w", err)
	}
	value.Version = 1
	value.PrivateKey = base64.StdEncoding.EncodeToString(value.key.Seed())
	value.CoreKey = base64.StdEncoding.EncodeToString(value.coreKey)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("create identity file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := temporary.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(dir, identityFileName))
}
