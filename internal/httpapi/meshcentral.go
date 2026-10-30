package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nerve-labs/nerve/internal/authz"
	"github.com/nerve-labs/nerve/internal/integrations/meshcentral"
)

// RunMeshCentralSync keeps the MeshCentral node listing current. It returns at
// once when no service account is configured.
func (s *Server) RunMeshCentralSync(ctx context.Context) {
	s.meshcentral.Run(ctx, s.cfg.MeshCentralSyncInterval, s.logger)
}

func (s *Server) meshCentralStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsView); !ok {
		return
	}
	status, err := s.meshcentral.Status(r.Context())
	if err != nil {
		s.logger.Error("read meshcentral status", "error", err)
		writeError(w, http.StatusInternalServerError, "meshcentral_unavailable", "MeshCentral state could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) syncMeshCentral(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePermission(w, r, authz.AgentsManage); !ok {
		return
	}
	if !s.meshcentral.Configured() {
		writeError(w, http.StatusConflict, "meshcentral_not_configured", "No MeshCentral service account is configured.")
		return
	}
	if err := s.meshcentral.Sync(r.Context()); err != nil {
		// Client errors are sentinel-based and never carry credentials.
		s.logger.Warn("meshcentral sync failed", "error", err)
		writeError(w, http.StatusBadGateway, "meshcentral_sync_failed", err.Error())
		return
	}
	s.meshCentralStatus(w, r)
}

func (s *Server) updateMeshCentralBinding(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.AgentsManage)
	if !ok {
		return
	}
	deviceID := r.PathValue("deviceID")
	if !isUUID(deviceID) {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID.")
		return
	}
	var input struct {
		Action string `json:"action"`
		NodeID string `json:"nodeId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !meshcentral.ValidNodeID(input.NodeID) {
		writeError(w, http.StatusBadRequest, "invalid_node_id", "Node ID must have the form node/<domain>/<id>.")
		return
	}
	actor := meshcentral.Actor{UserID: user.ID, SourceAddress: s.clientAddress(r)}
	var err error
	switch input.Action {
	case "confirm":
		err = s.meshcentral.Confirm(r.Context(), deviceID, input.NodeID, actor)
	case "unbind":
		err = s.meshcentral.Unbind(r.Context(), deviceID, input.NodeID, actor)
	default:
		writeError(w, http.StatusBadRequest, "invalid_action", "Action must be confirm or unbind.")
		return
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, meshcentral.ErrNodeNotFound):
		writeError(w, http.StatusNotFound, "node_not_found", "The MeshCentral node is not in the latest listing.")
	case errors.Is(err, meshcentral.ErrDeviceNotFound):
		writeError(w, http.StatusNotFound, "device_not_found", "Device not found.")
	case errors.Is(err, meshcentral.ErrNotBound):
		writeError(w, http.StatusNotFound, "not_bound", "The node is not bound to this device.")
	case errors.Is(err, meshcentral.ErrAlreadyBound):
		writeError(w, http.StatusConflict, "already_bound", "The node is already bound to a device.")
	case errors.Is(err, meshcentral.ErrWeakEvidence):
		writeError(w, http.StatusConflict, "weak_evidence", "No MAC address of the node matches this device; hostname evidence alone is not enough.")
	default:
		s.logger.Error("update meshcentral binding", "error", err)
		writeError(w, http.StatusInternalServerError, "binding_failed", "The binding could not be changed.")
	}
}
