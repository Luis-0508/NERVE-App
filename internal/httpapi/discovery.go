package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/authz"
	"github.com/nerve-labs/nerve/internal/discovery"
)

func (s *Server) discoveryReady(w http.ResponseWriter, r *http.Request) {
	if !s.databaseAvailable(w) {
		return
	}
	if !s.discovery.AgentReady(r.Context()) {
		writeError(w, http.StatusServiceUnavailable, "discovery_agent_unavailable", "The local discovery agent is not connected.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) discoveryState(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	if err := s.discovery.RecoverStaleScans(r.Context()); err != nil {
		s.logger.Warn("recover stale discovery scans", "error", err)
	}
	state, err := s.discovery.State(r.Context())
	if err != nil {
		s.logger.Error("query discovery state", "error", err)
		writeError(w, http.StatusInternalServerError, "discovery_unavailable", "Discovery status could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) saveDiscoveryTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.DiscoveryManage)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
		CIDR string `json:"cidr"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	target, err := s.discovery.SaveTarget(r.Context(), input.Name, input.CIDR, user.ID, s.clientAddress(r))
	if errors.Is(err, discovery.ErrInvalidCIDR) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_subnet", err.Error())
		return
	}
	if err != nil {
		s.logger.Error("save discovery target", "error", err)
		writeError(w, http.StatusInternalServerError, "discovery_target_failed", "The discovery subnet could not be saved.")
		return
	}
	writeJSON(w, http.StatusOK, target)
}

func (s *Server) queueDiscoveryScan(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.DiscoveryManage)
	if !ok {
		return
	}
	var input struct {
		TargetID     string `json:"targetId"`
		CancelActive bool   `json:"cancelActive,omitempty"`
	}
	if err := decodeJSON(r, &input); err != nil || !isUUID(input.TargetID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "A valid discovery target is required.")
		return
	}

	if input.CancelActive {
		scan, err := s.discovery.CancelActiveScan(r.Context(), input.TargetID, user.ID, s.clientAddress(r))
		if errors.Is(err, discovery.ErrNoJob) {
			writeError(w, http.StatusConflict, "no_active_scan", "No active discovery scan exists.")
			return
		}
		if err != nil {
			s.logger.Error("cancel discovery scan", "error", err)
			writeError(w, http.StatusInternalServerError, "scan_cancel_failed", "The discovery scan could not be stopped.")
			return
		}
		writeJSON(w, http.StatusOK, scan)
		return
	}

	if err := s.discovery.RecoverStaleScans(r.Context()); err != nil {
		s.logger.Error("recover stale discovery scans before queue", "error", err)
		writeError(w, http.StatusInternalServerError, "scan_recovery_failed", "Old discovery scan state could not be recovered.")
		return
	}

	scan, err := s.discovery.Queue(r.Context(), input.TargetID, user.ID, s.clientAddress(r))
	if errors.Is(err, discovery.ErrScanActive) {
		writeError(w, http.StatusConflict, "scan_active", err.Error())
		return
	}
	if err != nil {
		s.logger.Error("queue discovery scan", "error", err)
		writeError(w, http.StatusInternalServerError, "scan_queue_failed", "The discovery scan could not be queued.")
		return
	}
	writeJSON(w, http.StatusAccepted, scan)
}


// discoveryHandler serves one discovery worker route. decode reads the request
// body with the limits of the path that authenticated the worker.
type discoveryHandler func(w http.ResponseWriter, r *http.Request, worker discovery.Worker, decode func(any) error)

// discoveryAgentRoute accepts two kinds of worker on the same route: a signed
// request from an enrolled agent (identity from the verified signature), and,
// while NERVE_DISCOVERY_LEGACY_TOKEN allows it, the deprecated shared bearer
// token with a self-asserted worker ID. A request carrying signature headers is
// never downgraded to the token path.
func (s *Server) discoveryAgentRoute(limit int64, handle discoveryHandler) http.HandlerFunc {
	signed := s.agentEndpoint(limit, func(w http.ResponseWriter, r *http.Request, principal agents.Principal, body []byte) {
		if !s.requireDiscoveryCapability(w, r, principal) {
			return
		}
		handle(w, r, discovery.EnrolledWorker(principal.AgentID), func(destination any) error { return decodeAgentBody(body, destination) })
	})
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(agentproto.HeaderSignature) != "" || r.Header.Get(agentproto.HeaderCredential) != "" {
			signed(w, r)
			return
		}
		if !s.requireDiscoveryAgent(w, r) {
			return
		}
		worker := discovery.LegacyWorker(strings.TrimSpace(r.Header.Get("X-NERVE-Agent-ID")))
		handle(w, r, worker, func(destination any) error { return decodeJSON(r, destination) })
	}
}

// requireDiscoveryCapability admits an enrolled agent only while it is
// approved and currently reports the discovery.scan capability.
func (s *Server) requireDiscoveryCapability(w http.ResponseWriter, r *http.Request, principal agents.Principal) bool {
	if principal.Status != "active" {
		writeError(w, http.StatusForbidden, "agent_not_active", "The agent is waiting for operator approval and cannot run discovery yet.")
		return false
	}
	agent, err := s.agents.Get(r.Context(), principal.AgentID)
	if err != nil {
		s.logger.Error("load discovery agent", "error", err, "agent_id", principal.AgentID)
		writeError(w, http.StatusInternalServerError, "agent_unavailable", "The agent could not be loaded.")
		return false
	}
	if !slices.Contains(agent.Capabilities, agentproto.CapabilityDiscoveryScan) {
		writeError(w, http.StatusForbidden, "capability_not_advertised", "The agent does not report the discovery.scan capability. Enable discovery in its configuration.")
		return false
	}
	return true
}

// validLegacyWorkerID refuses IDs that could collide with an enrolled agent's
// worker row, whose ID is the agent UUID.
func validLegacyWorkerID(id string) bool {
	return id != "" && len(id) <= 200 && !isUUID(id)
}

func (s *Server) discoveryAgentHeartbeat(w http.ResponseWriter, r *http.Request, worker discovery.Worker, decode func(any) error) {
	var input discovery.AgentHeartbeat
	if err := decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agent", "Valid discovery agent information is required.")
		return
	}
	if worker.AgentID == "" {
		// Legacy workers may send their ID only in the body.
		if worker.ID == "" {
			worker.ID = strings.TrimSpace(input.ID)
		}
		if !validLegacyWorkerID(worker.ID) {
			writeError(w, http.StatusBadRequest, "invalid_agent", "Valid discovery agent information is required.")
			return
		}
	}
	err := s.discovery.Heartbeat(r.Context(), worker, input)
	if errors.Is(err, discovery.ErrWorkerConflict) {
		writeError(w, http.StatusConflict, "worker_conflict", err.Error())
		return
	}
	if err != nil {
		s.logger.Error("record discovery heartbeat", "error", err)
		writeError(w, http.StatusInternalServerError, "agent_heartbeat_failed", "The discovery heartbeat could not be recorded.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoveryAgentClaim(w http.ResponseWriter, r *http.Request, worker discovery.Worker, _ func(any) error) {
	if worker.AgentID == "" && !validLegacyWorkerID(worker.ID) {
		writeError(w, http.StatusBadRequest, "invalid_agent", "The discovery agent ID is required.")
		return
	}
	job, err := s.discovery.Claim(r.Context(), worker)
	if errors.Is(err, discovery.ErrNoJob) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.logger.Error("claim discovery job", "error", err, "agent_id", worker.ID)
		writeError(w, http.StatusInternalServerError, "scan_claim_failed", "The discovery job could not be claimed.")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) discoveryAgentReport(w http.ResponseWriter, r *http.Request, worker discovery.Worker, decode func(any) error) {
	scanID := r.PathValue("scanID")
	if !isUUID(scanID) || (worker.AgentID == "" && !validLegacyWorkerID(worker.ID)) {
		writeError(w, http.StatusBadRequest, "invalid_job", "A valid discovery job and agent are required.")
		return
	}
	var report discovery.Report
	if err := decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report", err.Error())
		return
	}
	if err := s.discovery.Report(r.Context(), scanID, worker, report); err != nil {
		s.logger.Error("apply discovery report", "error", err, "scan_id", scanID, "agent_id", worker.ID)
		writeError(w, http.StatusUnprocessableEntity, "report_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoveryAgentComplete(w http.ResponseWriter, r *http.Request, worker discovery.Worker, decode func(any) error) {
	scanID := r.PathValue("scanID")
	if !isUUID(scanID) || (worker.AgentID == "" && !validLegacyWorkerID(worker.ID)) {
		writeError(w, http.StatusBadRequest, "invalid_job", "A valid discovery job and agent are required.")
		return
	}
	var completion discovery.Completion
	if err := decode(&completion); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_completion", err.Error())
		return
	}
	if err := s.discovery.Complete(r.Context(), scanID, worker, completion); err != nil {
		s.logger.Error("complete discovery scan", "error", err, "scan_id", scanID, "agent_id", worker.ID)
		writeError(w, http.StatusUnprocessableEntity, "completion_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireDiscoveryAgent checks the deprecated shared discovery token. The path
// is refused outright when NERVE_DISCOVERY_LEGACY_TOKEN is false.
func (s *Server) requireDiscoveryAgent(w http.ResponseWriter, r *http.Request) bool {
	if !s.cfg.DiscoveryLegacyToken {
		writeError(w, http.StatusUnauthorized, "legacy_discovery_disabled", "The shared discovery token is disabled. Enroll the discovery worker as an agent.")
		return false
	}
	expected := strings.TrimSpace(s.cfg.DiscoveryAgentToken)
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || provided == "" {
		writeError(w, http.StatusUnauthorized, "agent_authentication_required", "Discovery agent authentication is required.")
		return false
	}
	expectedHash := sha256.Sum256([]byte(expected))
	providedHash := sha256.Sum256([]byte(provided))
	if subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
		writeError(w, http.StatusUnauthorized, "agent_authentication_required", "Discovery agent authentication is required.")
		return false
	}
	s.legacyDiscoveryWarning.Do(func() {
		s.logger.Warn("a discovery worker uses the deprecated shared NERVE_DISCOVERY_AGENT_TOKEN; enroll it as an agent, then set NERVE_DISCOVERY_LEGACY_TOKEN=false")
	})
	return true
}
