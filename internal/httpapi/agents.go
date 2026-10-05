package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/audit"
	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/authz"
)

// requirePermission authenticates the browser session and checks one explicit
// permission. Denied requests are written to the audit log.
func (s *Server) requirePermission(w http.ResponseWriter, r *http.Request, permission authz.Permission) (auth.User, bool) {
	user, _, ok := s.requireSession(w, r)
	if !ok || !s.checkPermission(w, r, user, permission) {
		return auth.User{}, false
	}
	return user, true
}

func (s *Server) actor(r *http.Request, user auth.User) agents.Actor {
	return agents.Actor{UserID: user.ID, SourceAddress: s.clientAddress(r)}
}

// ---------------------------------------------------------------------------
// Operator API (browser session + permission)

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsView); !ok {
		return
	}
	deviceID := r.URL.Query().Get("deviceId")
	if deviceID != "" && !isUUID(deviceID) {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID.")
		return
	}
	items, err := s.agents.List(r.Context(), agents.ListFilter{DeviceID: deviceID, IncludeRemoved: r.URL.Query().Get("includeRemoved") == "true"})
	if err != nil {
		s.logger.Error("list agents", "error", err)
		writeError(w, http.StatusInternalServerError, "agents_unavailable", "Agents could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) agentDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsView); !ok {
		return
	}
	agentID := r.PathValue("agentID")
	if !isUUID(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID must be a UUID.")
		return
	}
	agent, err := s.agents.Get(r.Context(), agentID)
	if s.writeAgentError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	s.endAgentCredential(w, r, false)
}

func (s *Server) removeAgent(w http.ResponseWriter, r *http.Request) {
	s.endAgentCredential(w, r, true)
}

func (s *Server) endAgentCredential(w http.ResponseWriter, r *http.Request, remove bool) {
	user, ok := s.requirePermission(w, r, authz.AgentsManage)
	if !ok {
		return
	}
	agentID := r.PathValue("agentID")
	if !isUUID(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID must be a UUID.")
		return
	}
	var (
		agent agents.Agent
		err   error
	)
	if remove {
		agent, err = s.agents.Remove(r.Context(), agentID, s.actor(r, user), s.agentLifecycleHooks...)
	} else {
		agent, err = s.agents.Revoke(r.Context(), agentID, s.actor(r, user), s.agentLifecycleHooks...)
	}
	if s.writeAgentError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) listEnrollments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsManage); !ok {
		return
	}
	items, err := s.agents.ListEnrollments(r.Context(), 100)
	if err != nil {
		s.logger.Error("list enrollments", "error", err)
		writeError(w, http.StatusInternalServerError, "enrollments_unavailable", "Enrollments could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createEnrollment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.AgentsManage)
	if !ok {
		return
	}
	var input struct {
		DisplayName      string `json:"displayName"`
		DeviceID         string `json:"deviceId"`
		AgentID          string `json:"agentId"`
		ExpiresInMinutes int    `json:"expiresInMinutes"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if (input.DeviceID != "" && !isUUID(input.DeviceID)) || (input.AgentID != "" && !isUUID(input.AgentID)) {
		writeError(w, http.StatusBadRequest, "invalid_request", "Device and agent IDs must be UUIDs.")
		return
	}
	enrollment, token, err := s.agents.CreateEnrollment(r.Context(), agents.CreateEnrollmentInput{
		DisplayName: input.DisplayName, DeviceID: input.DeviceID, TargetAgentID: input.AgentID,
		TTL: time.Duration(input.ExpiresInMinutes) * time.Minute,
	}, s.actor(r, user))
	if s.writeAgentError(w, err) {
		return
	}
	// The token is returned exactly once and never stored or logged.
	writeJSON(w, http.StatusCreated, map[string]any{"enrollment": enrollment, "token": token})
}

func (s *Server) revokeEnrollment(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.AgentsManage)
	if !ok {
		return
	}
	enrollmentID := r.PathValue("enrollmentID")
	if !isUUID(enrollmentID) {
		writeError(w, http.StatusBadRequest, "invalid_enrollment_id", "Enrollment ID must be a UUID.")
		return
	}
	enrollment, err := s.agents.RevokeEnrollment(r.Context(), enrollmentID, s.actor(r, user))
	if s.writeAgentError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, enrollment)
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AuditView); !ok {
		return
	}
	query := r.URL.Query()
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	records, err := audit.List(r.Context(), s.db, audit.Filter{TargetType: query.Get("targetType"), TargetID: query.Get("targetId"),
		ActionPrefix: query.Get("action"), Limit: limit})
	if err != nil {
		s.logger.Error("query audit log", "error", err)
		writeError(w, http.StatusInternalServerError, "audit_unavailable", "The audit log could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": records})
}

func (s *Server) writeAgentError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, agents.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "The agent does not exist.")
	case errors.Is(err, agents.ErrEnrollmentNotFound):
		writeError(w, http.StatusNotFound, "enrollment_not_found", "The enrollment does not exist.")
	case errors.Is(err, agents.ErrDeviceNotFound):
		writeError(w, http.StatusUnprocessableEntity, "device_not_found", err.Error())
	case errors.Is(err, agents.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", err.Error())
	case errors.Is(err, agents.ErrAgentRemoved):
		writeError(w, http.StatusConflict, "agent_removed", err.Error())
	case errors.Is(err, agents.ErrEnrollmentNotActive):
		writeError(w, http.StatusConflict, "enrollment_not_pending", err.Error())
	case errors.Is(err, agents.ErrCredentialRevoked):
		writeError(w, http.StatusConflict, "agent_already_revoked", "The agent has no active credential.")
	case errors.Is(err, agents.ErrCoreKeyMissing):
		writeError(w, http.StatusServiceUnavailable, "core_signing_key_missing", err.Error())
	case errors.Is(err, agents.ErrDeviceHasAgent):
		writeError(w, http.StatusConflict, "device_has_agent", err.Error())
	default:
		s.logger.Error("agent operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "agent_operation_failed", "The agent operation could not be completed.")
	}
	return true
}

// ---------------------------------------------------------------------------
// Agent API (Ed25519-signed requests; never browser sessions)

type agentHandlerFunc func(w http.ResponseWriter, r *http.Request, principal agents.Principal, body []byte)

const maxAgentRequestBytes = 1 << 20

// agentEndpoint authenticates a signed agent request. Browser-originated
// requests are refused outright, and session cookies are never consulted.
// Every response, including errors, is signed by Core (see coreSigned).
func (s *Server) agentEndpoint(limit int64, next agentHandlerFunc) http.HandlerFunc {
	return s.coreSigned(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
			writeError(w, http.StatusForbidden, "browser_request_rejected", "Agent endpoints do not accept browser requests.")
			return
		}
		if !s.databaseAvailable(w) {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The agent request exceeds the size limit.")
			return
		}
		headers, err := agentproto.ParseHeaders(r.Header)
		if err != nil {
			writeError(w, http.StatusUnauthorized, agentproto.CodeAuthenticationFailed, "Agent authentication is required.")
			return
		}
		principal, err := s.agents.Authenticate(r.Context(), headers, r.Method, agentproto.Route(r.URL.Path, r.URL.RawQuery), body, s.clientAddress(r))
		if err != nil {
			s.writeAgentAuthError(w, err)
			return
		}
		next(w, r, principal, body)
	})
}

func (s *Server) writeAgentAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentproto.ErrClockSkew):
		w.Header().Set(agentproto.HeaderServerTime, strconv.FormatInt(time.Now().Unix(), 10))
		writeError(w, http.StatusUnauthorized, agentproto.CodeClockSkew, "The agent clock differs from the server by more than the allowed skew.")
	case errors.Is(err, agents.ErrCredentialRevoked):
		writeError(w, http.StatusUnauthorized, agentproto.CodeCredentialRevoked, "The agent credential was revoked. Re-enroll the agent.")
	case errors.Is(err, agents.ErrReplay):
		writeError(w, http.StatusUnauthorized, agentproto.CodeReplay, "The agent request was already processed.")
	case errors.Is(err, agents.ErrAuthentication):
		writeError(w, http.StatusUnauthorized, agentproto.CodeAuthenticationFailed, "Agent authentication failed.")
	default:
		s.logger.Error("agent authentication error", "error", err)
		writeError(w, http.StatusInternalServerError, "agent_authentication_unavailable", "Agent authentication is temporarily unavailable.")
	}
}

func decodeAgentBody(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

// agentEnroll consumes a one-time enrollment token. The request must be signed
// with the private key matching the submitted public key.
func (s *Server) agentEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		writeError(w, http.StatusForbidden, "browser_request_rejected", "Agent endpoints do not accept browser requests.")
		return
	}
	if !s.databaseAvailable(w) {
		return
	}
	address := s.clientAddress(r)
	if !s.allowEnrollment(address) {
		w.Header().Set("Retry-After", "900")
		writeError(w, http.StatusTooManyRequests, "enrollment_rate_limited", "Too many enrollment attempts. Try again in 15 minutes.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentproto.MaxEnrollRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The enrollment request is too large.")
		return
	}
	var input agentproto.EnrollRequest
	if err := decodeAgentBody(body, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	publicKey, err := agentproto.DecodePublicKey(input.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key", err.Error())
		return
	}
	headers, err := agentproto.ParseHeaders(r.Header)
	if err != nil || headers.CredentialID != agentproto.CredentialSelfSigned {
		writeError(w, http.StatusUnauthorized, agentproto.CodeAuthenticationFailed, "The enrollment request must be signed with the enrolling key.")
		return
	}
	if err := headers.CheckTimestamp(time.Now()); err != nil {
		s.writeAgentAuthError(w, err)
		return
	}
	if err := headers.Verify(publicKey, r.Method, agentproto.Route(r.URL.Path, r.URL.RawQuery), body); err != nil {
		writeError(w, http.StatusUnauthorized, agentproto.CodeAuthenticationFailed, "The enrollment proof of possession is invalid.")
		return
	}
	result, err := s.agents.Enroll(r.Context(), input.Token, publicKey, input.Host, address)
	switch {
	case errors.Is(err, agents.ErrEnrollmentRejected):
		writeError(w, http.StatusUnauthorized, agentproto.CodeEnrollmentRejected, err.Error())
		return
	case errors.Is(err, agents.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_host_facts", err.Error())
		return
	case errors.Is(err, agents.ErrKeyReuse):
		writeError(w, http.StatusConflict, "public_key_reused", err.Error())
		return
	case err != nil:
		s.logger.Error("agent enrollment failed", "error", err)
		writeError(w, http.StatusInternalServerError, "enrollment_failed", "The agent could not be enrolled.")
		return
	}
	s.logger.Info("agent enrolled", "agent_id", result.AgentID, "fingerprint", result.Fingerprint)
	writeJSON(w, http.StatusCreated, agentproto.EnrollResponse{AgentID: result.AgentID, CredentialID: result.CredentialID,
		Fingerprint: result.Fingerprint, HeartbeatIntervalSeconds: int(agents.HeartbeatInterval.Seconds())})
}

func (s *Server) agentIdentity(w http.ResponseWriter, _ *http.Request, principal agents.Principal, _ []byte) {
	writeJSON(w, http.StatusOK, agentproto.IdentityResponse{AgentID: principal.AgentID, CredentialID: principal.CredentialID, Status: "active"})
}

func (s *Server) allowEnrollment(address string) bool {
	return s.allowAttempt(s.enrollAttempts, address, 30)
}

// RunMaintenance prunes expired replay-protection state and runs registered
// periodic tasks until ctx is cancelled.
func (s *Server) RunMaintenance(ctx context.Context, interval time.Duration) {
	if s.db == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) maintain(ctx context.Context) {
	taskCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.agents.PruneNonces(taskCtx); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn("agent maintenance failed", "error", err)
	}
	for _, task := range s.maintenanceTasks {
		if err := task(taskCtx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("maintenance task failed", "error", err)
		}
	}
}

func permissionList(set authz.Set) []string {
	items := make([]string, 0, len(set))
	for permission := range set {
		items = append(items, string(permission))
	}
	sort.Strings(items)
	return items
}

func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request, principal agents.Principal, body []byte) {
	var input agentproto.HeartbeatRequest
	if err := decodeAgentBody(body, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	err := s.agents.Heartbeat(r.Context(), principal, input)
	switch {
	case errors.Is(err, agents.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_heartbeat", err.Error())
		return
	case errors.Is(err, agents.ErrCredentialRevoked):
		writeError(w, http.StatusUnauthorized, agentproto.CodeCredentialRevoked, "The agent credential was revoked. Re-enroll the agent.")
		return
	case err != nil:
		s.logger.Error("record agent heartbeat", "error", err, "agent_id", principal.AgentID)
		writeError(w, http.StatusInternalServerError, "heartbeat_failed", "The heartbeat could not be recorded.")
		return
	}
	writeJSON(w, http.StatusOK, agentproto.HeartbeatResponse{ServerTime: time.Now().Unix(), HeartbeatIntervalSeconds: int(agents.HeartbeatInterval.Seconds())})
}

func (s *Server) updateAgentAssociation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.AgentsManage)
	if !ok {
		return
	}
	agentID := r.PathValue("agentID")
	if !isUUID(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID must be a UUID.")
		return
	}
	var input struct {
		Action   string `json:"action"`
		DeviceID string `json:"deviceId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	agent, err := s.agents.UpdateAssociation(r.Context(), agentID, agents.AssociationAction{Action: input.Action, DeviceID: input.DeviceID}, s.actor(r, user))
	if s.writeAgentError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// deviceRemote lists remote-management endpoints for a device across all
// providers, so the UI never needs to know which provider serves a capability.
func (s *Server) deviceRemote(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsView); !ok {
		return
	}
	deviceID := r.PathValue("deviceID")
	if !isUUID(deviceID) {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID.")
		return
	}
	endpoints, err := s.remote.DeviceEndpoints(r.Context(), deviceID)
	if err != nil {
		s.logger.Error("list device remote endpoints", "error", err)
		writeError(w, http.StatusInternalServerError, "remote_unavailable", "Remote-management state could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": endpoints})
}

// bufferedResponse captures a handler's response so it can be signed before
// anything reaches the client.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header            { return b.header }
func (b *bufferedResponse) WriteHeader(status int)         { b.status = status }
func (b *bufferedResponse) Write(data []byte) (int, error) { return b.body.Write(data) }

// coreSigned authenticates every agent-API response with Core's Ed25519 key,
// bound to the request's own signature, so agents can reject forged commands
// and forged status answers even over plain HTTP. Without a configured key the
// agent API is unavailable rather than unauthenticated.
func (s *Server) coreSigned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := s.agents.CoreSigningKey()
		if key == nil {
			writeError(w, http.StatusServiceUnavailable, "core_signing_key_missing", agents.ErrCoreKeyMissing.Error())
			return
		}
		recorded := &bufferedResponse{header: w.Header(), status: http.StatusOK}
		next(recorded, r)
		agentproto.SignCoreResponse(w.Header(), key, r.Header.Get(agentproto.HeaderSignature), recorded.status, recorded.body.Bytes())
		w.WriteHeader(recorded.status)
		_, _ = w.Write(recorded.body.Bytes())
	}
}
