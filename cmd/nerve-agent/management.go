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
	"sync/atomic"
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
	coreKey      ed25519.PublicKey
	http         *http.Client
	now          func() time.Time
}

func newManagementClient(apiURL string, id *identity) *managementClient {
	return newManagementClientWith(apiURL, id, nil)
}

// newManagementClientWith uses transport (nil: the default) for TLS settings
// such as a private CA.
func newManagementClientWith(apiURL string, id *identity, transport http.RoundTripper) *managementClient {
	return &managementClient{apiURL: strings.TrimRight(apiURL, "/"), credentialID: id.CredentialID, key: id.key, coreKey: id.coreKey,
		http: &http.Client{Timeout: 70 * time.Second, Transport: transport, CheckRedirect: refuseDowngrade}, now: time.Now}
}

// refuseDowngrade stops a redirect from an https Core URL to plain HTTP, so
// the reported transport always describes the connection actually used.
func refuseDowngrade(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
		return errors.New("refusing a redirect from HTTPS to HTTP")
	}
	return nil
}

// transport is what heartbeats report: https only for an https API URL. The
// agent never disables certificate verification, so https means verified TLS.
func (c *managementClient) transport() string {
	if strings.HasPrefix(strings.ToLower(c.apiURL), "https://") {
		return agentproto.TransportHTTPS
	}
	return agentproto.TransportHTTP
}

// post sends a signed JSON request to a route under /api/v1 (for example
// "/agent/heartbeat") and accepts the answer only if it is signed by the
// pinned Core key. A 204 response leaves destination untouched.
func (c *managementClient) post(ctx context.Context, route string, payload, destination any) (int, error) {
	verify := func(response *http.Response, requestSignature string, body []byte) error {
		return agentproto.VerifyCoreResponse(c.coreKey, response.Header, requestSignature, response.StatusCode, body)
	}
	return signedPost(ctx, c.http, c.apiURL, route, c.credentialID, c.key, payload, destination, c.now(), verify)
}

// responseVerifier authenticates Core's answer before the agent acts on it.
type responseVerifier func(response *http.Response, requestSignature string, body []byte) error

func signedPost(ctx context.Context, client *http.Client, apiURL, route, credentialID string, key ed25519.PrivateKey, payload, destination any, now time.Time, verify responseVerifier) (int, error) {
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
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read Core response: %w", err)
	}
	// An unverifiable response is treated like a network failure: it is never
	// acted on, and a forged "revoked" answer cannot stop the agent.
	if err := verify(response, request.Header.Get(agentproto.HeaderSignature), raw); err != nil {
		return 0, fmt.Errorf("%s (HTTP %d): %w", route, response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope agentproto.ErrorResponse
		_ = json.Unmarshal(raw, &envelope)
		return response.StatusCode, &apiError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if destination != nil && response.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(raw, destination); err != nil {
			return response.StatusCode, fmt.Errorf("decode Core response: %w", err)
		}
	}
	return response.StatusCode, nil
}

// enroll registers a freshly generated key pair with a one-time token and pins
// Core's key: the token carries its fingerprint, so a network attacker cannot
// substitute their own key during enrollment.
func enroll(ctx context.Context, apiURL, token string) (*identity, error) {
	return enrollWith(ctx, apiURL, token, nil)
}

func enrollWith(ctx context.Context, apiURL, token string, transport http.RoundTripper) (*identity, error) {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		return nil, errors.New("the Core API URL is required")
	}
	parsed, err := agentproto.ParseEnrollmentToken(token)
	if err != nil {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	var (
		response agentproto.EnrollResponse
		coreKey  ed25519.PublicKey
	)
	verify := func(response *http.Response, requestSignature string, body []byte) error {
		key, err := agentproto.CoreKeyFromResponse(response.Header, parsed.CoreFingerprint)
		if err != nil {
			return err
		}
		if err := agentproto.VerifyCoreResponse(key, response.Header, requestSignature, response.StatusCode, body); err != nil {
			return err
		}
		coreKey = key
		return nil
	}
	request := agentproto.EnrollRequest{Token: strings.TrimSpace(token), PublicKey: agentproto.EncodePublicKey(public), Host: collectHostFacts()}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	if _, err := signedPost(ctx, client, apiURL, "/agent/enroll", agentproto.CredentialSelfSigned, private, request, &response, time.Now(), verify); err != nil {
		return nil, err
	}
	if response.Fingerprint != agentproto.Fingerprint(public) {
		return nil, errors.New("core acknowledged a different public key; enrollment aborted")
	}
	enrolledAt := time.Now().UTC()
	return &identity{AgentID: response.AgentID, CredentialID: response.CredentialID, Fingerprint: response.Fingerprint,
		APIURL: apiURL, EnrolledAt: enrolledAt, CredentialCreatedAt: enrolledAt, key: private, coreKey: coreKey,
		deploymentKey: parsed.Deployment, pendingApproval: response.PendingApproval}, nil
}

// manager runs the management loops for an enrolled agent.
type manager struct {
	client            *managementClient
	agentID           string
	logger            *slog.Logger
	heartbeatInterval time.Duration
	capabilities      func() []string
	facts             func() agentproto.HostFacts
	// runtime describes the process for heartbeats (nil: not reported).
	runtime func(stopping bool) *agentproto.AgentRuntime
	// collectors overrides the built-in collectors (tests only).
	collectors map[string]collector

	mu        sync.Mutex
	lastError string
	executed  map[string]time.Time
	// legacyCore is set when Core rejects the runtime field (older releases).
	legacyCore atomic.Bool
	// legacyTransport is set when Core rejects the transport field.
	legacyTransport atomic.Bool
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
	sent := m.currentError()
	request := agentproto.HeartbeatRequest{Host: m.facts(), Capabilities: m.capabilities(), LastError: sent}
	if m.runtime != nil && !m.legacyCore.Load() {
		request.Runtime = m.runtime(false)
	}
	if !m.legacyTransport.Load() {
		request.Transport = m.client.transport()
	}
	var response agentproto.HeartbeatResponse
	_, err := m.client.post(ctx, "/agent/heartbeat", request, &response)
	// Core releases before a field reject the first unknown one; drop it and
	// retry until none is left.
	for {
		if request.Transport != "" && rejectsField(err, "transport") {
			m.logger.Info("Core does not accept transport reports; heartbeats continue without them")
			m.legacyTransport.Store(true)
			request.Transport = ""
		} else if request.Runtime != nil && rejectsField(err, "runtime") {
			m.logger.Info("Core does not accept runtime reports; heartbeats continue without them")
			m.legacyCore.Store(true)
			request.Runtime = nil
		} else {
			break
		}
		_, err = m.client.post(ctx, "/agent/heartbeat", request, &response)
	}
	if err == nil {
		m.clearError(sent)
	}
	return err
}

// clearError forgets the reported error unless another loop recorded a newer
// one while the heartbeat was in flight.
func (m *manager) clearError(reported string) {
	m.mu.Lock()
	if m.lastError == reported {
		m.lastError = ""
	}
	m.mu.Unlock()
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

// rejectsField recognizes an older Core refusing an unknown heartbeat field.
func rejectsField(err error, field string) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest && apiErr.Code == "invalid_request" &&
		strings.Contains(apiErr.Message, `"`+field+`"`)
}
