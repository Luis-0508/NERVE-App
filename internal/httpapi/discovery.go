package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

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
	user, _, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	if user.Role != "administrator" {
		writeError(w, http.StatusForbidden, "administrator_required", "Administrator access is required.")
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
	target, err := s.discovery.SaveTarget(r.Context(), input.Name, input.CIDR, user.ID)
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
	user, _, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	if user.Role != "administrator" {
		writeError(w, http.StatusForbidden, "administrator_required", "Administrator access is required.")
		return
	}
	var input struct {
		TargetID    string `json:"targetId"`
		CancelActive bool   `json:"cancelActive,omitempty"`
	}
	if err := decodeJSON(r, &input); err != nil || !isUUID(input.TargetID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "A valid discovery target is required.")
		return
	}

	if input.CancelActive {
		scan, err := s.discovery.CancelActiveScan(r.Context(), input.TargetID, user.ID)
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

	scan, err := s.discovery.Queue(r.Context(), input.TargetID, user.ID)
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

func (s *Server) discoveryAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.requireDiscoveryAgent(w, r) {
		return
	}
	var input discovery.AgentHeartbeat
	if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.ID) == "" || len(input.ID) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_agent", "Valid discovery agent information is required.")
		return
	}
	if err := s.discovery.Heartbeat(r.Context(), input); err != nil {
		s.logger.Error("record discovery heartbeat", "error", err)
		writeError(w, http.StatusInternalServerError, "agent_heartbeat_failed", "The discovery heartbeat could not be recorded.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoveryAgentClaim(w http.ResponseWriter, r *http.Request) {
	if !s.requireDiscoveryAgent(w, r) {
		return
	}
	agentID := strings.TrimSpace(r.Header.Get("X-NERVE-Agent-ID"))
	if agentID == "" || len(agentID) > 200 {
		writeError(w, http.StatusBadRequest, "invalid_agent", "The discovery agent ID is required.")
		return
	}
	job, err := s.discovery.Claim(r.Context(), agentID)
	if errors.Is(err, discovery.ErrNoJob) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.logger.Error("claim discovery job", "error", err, "agent_id", agentID)
		writeError(w, http.StatusInternalServerError, "scan_claim_failed", "The discovery job could not be claimed.")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) discoveryAgentReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireDiscoveryAgent(w, r) {
		return
	}
	scanID := r.PathValue("scanID")
	agentID := strings.TrimSpace(r.Header.Get("X-NERVE-Agent-ID"))
	if !isUUID(scanID) || agentID == "" {
		writeError(w, http.StatusBadRequest, "invalid_job", "A valid discovery job and agent are required.")
		return
	}
	var report discovery.Report
	if err := decodeJSON(r, &report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report", err.Error())
		return
	}
	if err := s.discovery.Report(r.Context(), scanID, agentID, report); err != nil {
		s.logger.Error("apply discovery report", "error", err, "scan_id", scanID, "agent_id", agentID)
		writeError(w, http.StatusUnprocessableEntity, "report_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoveryAgentComplete(w http.ResponseWriter, r *http.Request) {
	if !s.requireDiscoveryAgent(w, r) {
		return
	}
	scanID := r.PathValue("scanID")
	agentID := strings.TrimSpace(r.Header.Get("X-NERVE-Agent-ID"))
	if !isUUID(scanID) || agentID == "" {
		writeError(w, http.StatusBadRequest, "invalid_job", "A valid discovery job and agent are required.")
		return
	}
	var completion discovery.Completion
	if err := decodeJSON(r, &completion); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_completion", err.Error())
		return
	}
	if err := s.discovery.Complete(r.Context(), scanID, agentID, completion); err != nil {
		s.logger.Error("complete discovery scan", "error", err, "scan_id", scanID, "agent_id", agentID)
		writeError(w, http.StatusUnprocessableEntity, "completion_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireDiscoveryAgent(w http.ResponseWriter, r *http.Request) bool {
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
	return true
}
