package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/audit"
	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/authz"
	"github.com/nerve-labs/nerve/internal/remote"
)

// checkPermission enforces a permission that depends on request content (for
// example the risk class of a command type) after the session is known.
func (s *Server) checkPermission(w http.ResponseWriter, r *http.Request, user auth.User, permission authz.Permission) bool {
	if user.Can(permission) {
		return true
	}
	// Only state-changing denials are audited, so polling UIs cannot flood the log.
	if !isUnsafe(r.Method) {
		writeError(w, http.StatusForbidden, "permission_required", "Your role does not grant the "+string(permission)+" permission.")
		return false
	}
	if err := audit.Write(r.Context(), s.db, audit.Entry{ActorUserID: user.ID, Action: "authz.denied", TargetType: "route",
		TargetID: r.Method + " " + r.URL.Path, Result: audit.ResultDenied, ErrorCode: "permission_required",
		SourceAddress: s.clientAddress(r), Parameters: map[string]any{"permission": string(permission)}}); err != nil {
		s.logger.Error("audit permission denial", "error", err)
	}
	writeError(w, http.StatusForbidden, "permission_required", "Your role does not grant the "+string(permission)+" permission.")
	return false
}

func (s *Server) createCommand(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("agentID")
	if !isUUID(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID must be a UUID.")
		return
	}
	var input struct {
		Type             string         `json:"type"`
		Parameters       map[string]any `json:"parameters"`
		TimeoutSeconds   int            `json:"timeoutSeconds"`
		ExpiresInSeconds int            `json:"expiresInSeconds"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	policy, enabled := remote.PolicyFor(input.Type)
	if !enabled {
		writeError(w, http.StatusUnprocessableEntity, "command_not_enabled", "This command type is not enabled in NERVE.")
		return
	}
	if !s.checkPermission(w, r, user, policy.Permission) {
		return
	}
	command, err := s.commands.Create(r.Context(), remote.CreateInput{AgentID: agentID, Type: input.Type, Parameters: input.Parameters,
		TimeoutSeconds: input.TimeoutSeconds, ExpiresInSeconds: input.ExpiresInSeconds}, s.actor(r, user))
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, command)
}

func (s *Server) listAgentCommands(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.RemoteView); !ok {
		return
	}
	agentID := r.PathValue("agentID")
	if !isUUID(agentID) {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "Agent ID must be a UUID.")
		return
	}
	items, err := s.commands.List(r.Context(), agentID, "", 50)
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listDeviceCommands(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.RemoteView); !ok {
		return
	}
	deviceID := r.PathValue("deviceID")
	if !isUUID(deviceID) {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID.")
		return
	}
	items, err := s.commands.List(r.Context(), "", deviceID, 50)
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) commandDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.RemoteView); !ok {
		return
	}
	commandID := r.PathValue("commandID")
	if !isUUID(commandID) {
		writeError(w, http.StatusBadRequest, "invalid_command_id", "Command ID must be a UUID.")
		return
	}
	command, err := s.commands.Get(r.Context(), commandID)
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, command)
}

func (s *Server) cancelCommand(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	commandID := r.PathValue("commandID")
	if !isUUID(commandID) {
		writeError(w, http.StatusBadRequest, "invalid_command_id", "Command ID must be a UUID.")
		return
	}
	existing, err := s.commands.Get(r.Context(), commandID)
	if s.writeCommandError(w, err) {
		return
	}
	// Cancelling needs the same permission as requesting that command type.
	permission := authz.RemoteExecutePrivileged
	if policy, ok := remote.PolicyFor(existing.Type); ok {
		permission = policy.Permission
	}
	if !s.checkPermission(w, r, user, permission) {
		return
	}
	command, err := s.commands.Cancel(r.Context(), commandID, s.actor(r, user))
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, command)
}

func (s *Server) writeCommandError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, agents.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "The agent does not exist.")
	case errors.Is(err, remote.ErrNotFound):
		writeError(w, http.StatusNotFound, agentproto.CodeCommandNotFound, "The command does not exist.")
	case errors.Is(err, remote.ErrUnknownCommand):
		writeError(w, http.StatusUnprocessableEntity, "command_not_enabled", err.Error())
	case errors.Is(err, remote.ErrInvalidParameters):
		writeError(w, http.StatusUnprocessableEntity, "invalid_parameters", err.Error())
	case errors.Is(err, remote.ErrCapabilityNotAdvertised):
		writeError(w, http.StatusConflict, "capability_not_supported", err.Error())
	case errors.Is(err, remote.ErrAgentUnavailable):
		writeError(w, http.StatusConflict, "agent_unavailable", err.Error())
	case errors.Is(err, remote.ErrQueueFull):
		writeError(w, http.StatusTooManyRequests, "command_queue_full", err.Error())
	case errors.Is(err, remote.ErrNotActive):
		writeError(w, http.StatusConflict, agentproto.CodeCommandNotActive, err.Error())
	case errors.Is(err, remote.ErrCancelled):
		writeError(w, http.StatusConflict, agentproto.CodeCommandCancelled, err.Error())
	case errors.Is(err, remote.ErrAlreadyCompleted):
		writeError(w, http.StatusConflict, agentproto.CodeCommandAlreadyCompleted, err.Error())
	case errors.Is(err, remote.ErrResultTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, agentproto.CodeResultTooLarge, err.Error())
	case errors.Is(err, remote.ErrInvalidResult):
		writeError(w, http.StatusUnprocessableEntity, "invalid_result", err.Error())
	default:
		s.logger.Error("remote command operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "command_operation_failed", "The command operation could not be completed.")
	}
	return true
}

// ---------------------------------------------------------------------------
// Agent side

func (s *Server) agentClaimCommand(w http.ResponseWriter, r *http.Request, principal agents.Principal, body []byte) {
	var input agentproto.ClaimRequest
	if len(body) > 0 {
		if err := decodeAgentBody(body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	command, err := s.commands.Claim(r.Context(), principal, time.Duration(input.WaitSeconds)*time.Second)
	if errors.Is(err, remote.ErrNoCommand) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, command)
}

func (s *Server) agentStartCommand(w http.ResponseWriter, r *http.Request, principal agents.Principal, _ []byte) {
	commandID := r.PathValue("commandID")
	if !isUUID(commandID) {
		writeError(w, http.StatusBadRequest, "invalid_command_id", "Command ID must be a UUID.")
		return
	}
	if s.writeCommandError(w, s.commands.Start(r.Context(), principal, commandID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentSubmitResult(w http.ResponseWriter, r *http.Request, principal agents.Principal, body []byte) {
	commandID := r.PathValue("commandID")
	if !isUUID(commandID) {
		writeError(w, http.StatusBadRequest, "invalid_command_id", "Command ID must be a UUID.")
		return
	}
	var result agentproto.CommandResult
	if err := decodeAgentBody(body, &result); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ack, err := s.commands.SubmitResult(r.Context(), principal, commandID, result)
	if s.writeCommandError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, ack)
}
