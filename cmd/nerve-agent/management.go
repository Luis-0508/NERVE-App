package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// apiError is a non-2xx response from Core.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Core returned HTTP %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("Core returned HTTP %d", e.Status)
}

// requiresReenrollment reports errors that no retry can fix.
func requiresReenrollment(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && (apiErr.Code == agentproto.CodeCredentialRevoked || apiErr.Code == agentproto.CodeAuthenticationFailed)
}

// managementClient signs every request with the agent's Ed25519 key.
type managementClient struct {
	apiURL       string
	credentialID string
	key          ed25519.PrivateKey
	http         *http.Client
	now          func() time.Time
}

func newManagementClient(apiURL string, id *identity) *managementClient {
	return &managementClient{apiURL: strings.TrimRight(apiURL, "/"), credentialID: id.CredentialID, key: id.key,
		http: &http.Client{Timeout: 70 * time.Second}, now: time.Now}
}

// post sends a signed JSON request to a route under /api/v1 (for example
// "/agent/heartbeat"). A 204 response leaves destination untouched.
func (c *managementClient) post(ctx context.Context, route string, payload, destination any) (int, error) {
	return signedPost(ctx, c.http, c.apiURL, route, c.credentialID, c.key, payload, destination, c.now())
}

func signedPost(ctx context.Context, client *http.Client, apiURL, route, credentialID string, key ed25519.PrivateKey, payload, destination any, now time.Time) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+route, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "nerve-agent/"+version)
	if err := agentproto.Sign(request, credentialID, key, body, now); err != nil {
		return 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope agentproto.ErrorResponse
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		_ = json.Unmarshal(raw, &envelope)
		return response.StatusCode, &apiError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if destination != nil && response.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(destination); err != nil {
			return response.StatusCode, fmt.Errorf("decode Core response: %w", err)
		}
	}
	return response.StatusCode, nil
}

// enroll registers a freshly generated key pair with a one-time token.
func enroll(ctx context.Context, apiURL, token string) (*identity, error) {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		return nil, errors.New("the Core API URL is required")
	}
	if _, _, err := agentproto.ParseEnrollmentToken(token); err != nil {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	var response agentproto.EnrollResponse
	request := agentproto.EnrollRequest{Token: strings.TrimSpace(token), PublicKey: agentproto.EncodePublicKey(public), Host: collectHostFacts()}
	client := &http.Client{Timeout: 30 * time.Second}
	if _, err := signedPost(ctx, client, apiURL, "/agent/enroll", agentproto.CredentialSelfSigned, private, request, &response, time.Now()); err != nil {
		return nil, err
	}
	if response.Fingerprint != agentproto.Fingerprint(public) {
		return nil, errors.New("core acknowledged a different public key; enrollment aborted")
	}
	return &identity{AgentID: response.AgentID, CredentialID: response.CredentialID, Fingerprint: response.Fingerprint,
		APIURL: apiURL, EnrolledAt: time.Now().UTC(), key: private}, nil
}

// manager runs the management loops for an enrolled agent.
type manager struct {
	client            *managementClient
	agentID           string
	logger            *slog.Logger
	heartbeatInterval time.Duration
	capabilities      func() []string
	facts             func() agentproto.HostFacts
	// collectors overrides the built-in collectors (tests only).
	collectors map[string]collector

	mu        sync.Mutex
	lastError string
	executed  map[string]time.Time
}

func (m *manager) setError(value string) {
	m.mu.Lock()
	m.lastError = value
	m.mu.Unlock()
}

func (m *manager) currentError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

func (m *manager) heartbeat(ctx context.Context) error {
	request := agentproto.HeartbeatRequest{Host: m.facts(), Capabilities: m.capabilities(), LastError: m.currentError()}
	var response agentproto.HeartbeatResponse
	_, err := m.client.post(ctx, "/agent/heartbeat", request, &response)
	return err
}

// runHeartbeats reports liveness until ctx ends. Failures back off with
// jitter; a revoked credential pauses the loop because only re-enrollment
// can fix it.
func (m *manager) runHeartbeats(ctx context.Context) {
	interval := m.heartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	failures := 0
	for {
		err := m.heartbeat(ctx)
		wait := interval
		switch {
		case err == nil:
			if failures > 0 {
				m.logger.Info("management heartbeat restored")
			}
			failures = 0
			m.setError("")
		case ctx.Err() != nil:
			return
		case requiresReenrollment(err):
			m.logger.Error("management credential rejected; re-enroll this agent", "error", err)
			wait = 10 * time.Minute
		default:
			failures++
			wait = backoff(failures)
			m.setError("heartbeat: " + err.Error())
			m.logger.Warn("management heartbeat failed", "error", err, "retry_in", wait.String())
		}
		if !sleepContext(ctx, wait) {
			return
		}
	}
}

// backoff grows exponentially from 2 s to 60 s with ±20% jitter.
func backoff(failures int) time.Duration {
	delay := 2 * time.Second
	for i := 1; i < failures && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	jitter := time.Duration(rand.Int64N(int64(delay)/5*2+1)) - delay/5
	return delay + jitter
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
