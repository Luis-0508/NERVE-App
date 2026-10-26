package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// pendingRotationFileName holds a new key until Core has confirmed it. It is
// written before the rotation request, so a crash at any point leaves enough
// state to find out whether Core committed the new credential.
const pendingRotationFileName = "identity.pending.json"

type pendingRotation struct {
	Version              int       `json:"version"`
	AgentID              string    `json:"agentId"`
	PreviousCredentialID string    `json:"previousCredentialId"`
	CredentialID         string    `json:"credentialId"`
	PrivateKey           string    `json:"privateKey"`
	CreatedAt            time.Time `json:"createdAt"`

	key ed25519.PrivateKey
}

// rotationCheckInterval is how often a running agent compares its key age
// with the configured maximum.
var rotationCheckInterval = time.Hour

func loadPendingRotation(dir string) (*pendingRotation, error) {
	path := filepath.Join(dir, pendingRotationFileName)
	if err := checkPrivateFile(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value pendingRotation
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse pending key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(value.PrivateKey)
	if err != nil || len(seed) != ed25519.SeedSize || value.Version != 1 || value.CredentialID == "" {
		return nil, errors.New("pending key file is invalid")
	}
	value.key = ed25519.NewKeyFromSeed(seed)
	return &value, nil
}

func removePendingRotation(dir string) error {
	err := os.Remove(filepath.Join(dir, pendingRotationFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// candidate is the identity the pending key would produce.
func (p *pendingRotation) candidate(current *identity) *identity {
	next := *current
	next.CredentialID = p.CredentialID
	next.Fingerprint = agentproto.Fingerprint(p.key.Public().(ed25519.PublicKey))
	next.CredentialCreatedAt = p.CreatedAt
	next.key = p.key
	next.deploymentKey, next.pendingApproval = false, false
	return &next
}

// promote makes the pending key the agent's identity.
func promote(dir string, current *identity, pending *pendingRotation) (*identity, error) {
	next := pending.candidate(current)
	if err := saveIdentity(dir, next); err != nil {
		return nil, fmt.Errorf("save rotated identity: %w", err)
	}
	if err := removePendingRotation(dir); err != nil {
		return nil, fmt.Errorf("remove pending key: %w", err)
	}
	return next, nil
}

// newCredentialID returns a random UUIDv4 in Core's lowercase format.
func newCredentialID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

// rotateCredential replaces the agent's key: the new key is saved as pending,
// registered with Core (signed with the old key, proven with the new one), and
// only then written to identity.json.
func rotateCredential(ctx context.Context, dir, apiURL string, id *identity, transport http.RoundTripper, now time.Time) (*identity, error) {
	if _, err := os.Stat(filepath.Join(dir, pendingRotationFileName)); err == nil {
		return nil, errors.New("an earlier key rotation is unresolved; it is resolved when Core is reachable")
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	credentialID, err := newCredentialID()
	if err != nil {
		return nil, fmt.Errorf("generate credential ID: %w", err)
	}
	pending := &pendingRotation{Version: 1, AgentID: id.AgentID, PreviousCredentialID: id.CredentialID, CredentialID: credentialID,
		PrivateKey: base64.StdEncoding.EncodeToString(key.Seed()), CreatedAt: now.UTC(), key: key}
	encoded, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writePrivateFile(dir, pendingRotationFileName, encoded); err != nil {
		return nil, fmt.Errorf("save pending key: %w", err)
	}

	request := agentproto.SignRotation(id.AgentID, id.CredentialID, credentialID, key, now)
	var response agentproto.RotateCredentialResponse
	status, err := newManagementClientWith(apiURL, id, transport).post(ctx, "/agent/credential/rotate", request, &response)
	if err != nil {
		// A signed client error means Core refused and stored nothing. Anything
		// else (network, timeout, server error) is ambiguous: keep the pending
		// key until a later check settles which credential Core holds.
		var apiErr *apiError
		if errors.As(err, &apiErr) && status >= 400 && status < 500 {
			_ = removePendingRotation(dir)
		}
		return nil, fmt.Errorf("rotate credential: %w", err)
	}
	if response.CredentialID != credentialID || response.Fingerprint != agentproto.Fingerprint(key.Public().(ed25519.PublicKey)) {
		return nil, errors.New("core confirmed a different credential; the pending key is kept for the next check")
	}
	if !response.CreatedAt.IsZero() {
		pending.CreatedAt = response.CreatedAt.UTC()
	}
	return promote(dir, id, pending)
}

// resolvePendingRotation settles a rotation that was interrupted. It asks Core
// which credential it accepts: the pending one (Core committed, the agent did
// not save) or the current one (Core never stored the new key). It returns
// the identity to use; on an unreachable Core it keeps both keys and errs.
func resolvePendingRotation(ctx context.Context, dir, apiURL string, id *identity, transport http.RoundTripper) (*identity, error) {
	pending, err := loadPendingRotation(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return id, nil
	}
	if err != nil {
		return id, err
	}
	if pending.AgentID != id.AgentID || pending.PreviousCredentialID != id.CredentialID {
		// The identity was replaced since (promotion already saved, or a
		// re-enrollment): the pending key belongs to nothing.
		return id, removePendingRotation(dir)
	}
	candidate := pending.candidate(id)
	var answer agentproto.IdentityResponse
	_, newErr := newManagementClientWith(apiURL, candidate, transport).post(ctx, "/agent/identity", struct{}{}, &answer)
	if newErr == nil && answer.CredentialID == pending.CredentialID {
		return promote(dir, id, pending)
	}
	if !isAPIError(newErr, agentproto.CodeAuthenticationFailed) {
		return id, fmt.Errorf("check pending key with Core: %w", newErr)
	}
	_, oldErr := newManagementClientWith(apiURL, id, transport).post(ctx, "/agent/identity", struct{}{}, &answer)
	if oldErr != nil {
		return id, fmt.Errorf("Core accepts neither the pending nor the current key: %w", oldErr)
	}
	return id, removePendingRotation(dir)
}

// formatKeyAge prints a key age in whole days (hours below one day).
func formatKeyAge(age time.Duration) string {
	if age < 24*time.Hour {
		return fmt.Sprintf("%d hours", int(age.Hours()))
	}
	return fmt.Sprintf("%d days", int(age.Hours()/24))
}

func isAPIError(err error, code string) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// rotateAndSettle rotates the key and, when the outcome is unclear (no or an
// unverifiable answer), asks Core right away which key it holds, so a failed
// attempt does not block later rotations until the next restart.
func rotateAndSettle(ctx context.Context, dir, apiURL string, id *identity, transport http.RoundTripper) (*identity, error) {
	next, err := rotateCredential(ctx, dir, apiURL, id, transport, time.Now())
	if err == nil || ctx.Err() != nil {
		return next, err
	}
	settled, settleErr := resolvePendingRotation(ctx, dir, apiURL, id, transport)
	if settleErr == nil && settled.CredentialID != id.CredentialID {
		return settled, nil
	}
	return nil, err
}

// rotateWhenDue rotates the key whenever it is older than maxAge and calls
// rotated after a successful rotation, so the management loops restart with
// the new key.
func rotateWhenDue(ctx context.Context, options managementOptions, apiURL string, id *identity, maxAge time.Duration, rotated func()) {
	for {
		if age := id.credentialAge(time.Now()); age >= maxAge {
			next, err := rotateAndSettle(ctx, options.stateDir, apiURL, id, options.transport)
			if err == nil {
				options.logger.Info("agent key rotated", "agent_id", next.AgentID, "credential_id", next.CredentialID,
					"fingerprint", next.Fingerprint, "previous_age", age.Round(time.Hour).String())
				rotated()
				return
			}
			if ctx.Err() != nil {
				return
			}
			options.logger.Warn("automatic key rotation failed; retrying later", "error", err, "retry_in", rotationCheckInterval.String())
		}
		if !sleepContext(ctx, rotationCheckInterval) {
			return
		}
	}
}

// settlePendingRotation resolves an interrupted rotation before management
// starts. It reports false when Core could not be asked yet; the caller then
// keeps the current key and retries with retryPendingRotation.
func settlePendingRotation(ctx context.Context, options managementOptions, apiURL string, id *identity, logger *slog.Logger) (*identity, bool) {
	next, err := resolvePendingRotation(ctx, options.stateDir, apiURL, id, options.transport)
	if err != nil {
		logger.Warn("interrupted key rotation not resolved yet; continuing with the current key", "error", err)
		return id, false
	}
	if next.CredentialID != id.CredentialID {
		logger.Info("completed an interrupted key rotation", "agent_id", next.AgentID, "credential_id", next.CredentialID)
	}
	return next, true
}

// retryPendingRotation keeps asking Core until the interrupted rotation is
// settled. A promoted key changes identity.json, which restarts management.
func retryPendingRotation(ctx context.Context, options managementOptions, apiURL string, id *identity, logger *slog.Logger) {
	for sleepContext(ctx, time.Minute) {
		if _, settled := settlePendingRotation(ctx, options, apiURL, id, logger); settled {
			return
		}
	}
}
