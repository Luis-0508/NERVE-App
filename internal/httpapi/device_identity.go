package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/authz"
	"github.com/nerve-labs/nerve/internal/devices"
)

func (s *Server) deviceActor(r *http.Request, user auth.User) devices.Actor {
	return devices.Actor{UserID: user.ID, SourceAddress: s.clientAddress(r)}
}

func (s *Server) writeDeviceError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, devices.ErrNotFound):
		writeError(w, http.StatusNotFound, "device_not_found", "The requested device does not exist.")
	case errors.Is(err, devices.ErrMerged):
		writeError(w, http.StatusConflict, "device_merged", err.Error())
	case errors.Is(err, devices.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_device_update", err.Error())
	default:
		s.logger.Error("device identity operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "device_operation_failed", "The device change could not be completed.")
	}
	return true
}

// optionalString decodes a JSON field that may be absent (no change), null or
// "" (clear the human-owned value) or a string (set it).
func optionalString(fields map[string]json.RawMessage, name string) (*string, error) {
	raw, present := fields[name]
	if !present {
		return nil, nil
	}
	empty := ""
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return &empty, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New(name + " must be a string or null")
	}
	return &value, nil
}

// updateDeviceMetadata edits human-owned fields. Only fields present in the
// body change; discovery never writes any of them.
func (s *Server) updateDeviceMetadata(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.DevicesManage)
	if !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := decoder.Decode(&fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
		return
	}
	var update devices.MetadataUpdate
	var err error
	for name := range fields {
		switch name {
		case "displayName", "notes", "owner", "location", "classification", "tags":
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown field "+name)
			return
		}
	}
	for name, target := range map[string]**string{"displayName": &update.DisplayName, "notes": &update.Notes,
		"owner": &update.Owner, "location": &update.Location, "classification": &update.Classification} {
		if *target, err = optionalString(fields, name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if raw, present := fields["tags"]; present {
		tags := []string{}
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if err := json.Unmarshal(raw, &tags); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "tags must be an array of strings")
				return
			}
		}
		update.Tags = &tags
	}
	if s.writeDeviceError(w, s.identity.UpdateMetadata(r.Context(), deviceID, update, s.deviceActor(r, user))) {
		return
	}
	device, err := s.inventory.Devices.Get(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "get updated device", err)
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (s *Server) setDeviceTrust(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requirePermission(w, r, authz.DevicesTrust)
	if !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.writeDeviceError(w, s.identity.SetTrust(r.Context(), deviceID, input.State, input.Reason, s.deviceActor(r, user))) {
		return
	}
	device, err := s.inventory.Devices.Get(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "get updated device", err)
		return
	}
	writeJSON(w, http.StatusOK, device)
}

// deviceVocabulary lists the values the UI may offer for operator fields.
func (s *Server) deviceVocabulary(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trustStates": devices.TrustStates, "classifications": devices.Classifications})
}
