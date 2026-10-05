package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/inventory"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	limit, err := integerQuery(r, "limit", 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be an integer.")
		return
	}
	offset, err := integerQuery(r, "offset", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_offset", "Offset must be an integer.")
		return
	}
	params, err := inventory.NormalizeListParams(inventory.DeviceListParams{
		Search:         r.URL.Query().Get("search"),
		Status:         r.URL.Query().Get("status"),
		Classification: r.URL.Query().Get("classification"),
		Trust:          r.URL.Query().Get("trust"),
		Tag:            r.URL.Query().Get("tag"),
		Sort:           r.URL.Query().Get("sort"),
		Direction:      r.URL.Query().Get("direction"),
		Limit:          limit,
		Offset:         offset,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_device_query", err.Error())
		return
	}
	result, err := s.inventory.Devices.List(r.Context(), params)
	if errors.Is(err, inventory.ErrInvalidSort) {
		writeError(w, http.StatusBadRequest, "invalid_sort", "Sort must be name, status, classification, trust, hostname, ip, vendor, first_seen, or last_seen.")
		return
	}
	if err != nil {
		s.inventoryFailure(w, "list devices", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) deviceFacets(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	result, err := s.inventory.Devices.Facets(r.Context())
	if err != nil {
		s.inventoryFailure(w, "list device facets", err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) deviceDetail(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	device, err := s.inventory.Devices.Get(r.Context(), deviceID)
	if errors.Is(err, inventory.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device_not_found", "The requested device does not exist.")
		return
	}
	if err != nil {
		s.inventoryFailure(w, "get device", err)
		return
	}
	interfaces, err := s.inventory.Interfaces.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device interfaces", err)
		return
	}
	addresses, err := s.inventory.IPAddresses.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device IP addresses", err)
		return
	}
	services, err := s.inventory.Services.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device services", err)
		return
	}
	relationships, err := s.inventory.Relationships.ListByEntity(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device relationships", err)
		return
	}
	facts, err := devices.ListAttributes(r.Context(), s.db, deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device facts", err)
		return
	}
	writeJSON(w, http.StatusOK, inventory.DeviceDetail{
		Device: device, Interfaces: interfaces, IPAddresses: addresses,
		Services: services, Relationships: relationships, Facts: facts,
	})
}

func (s *Server) deviceInterfaces(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	items, err := s.inventory.Interfaces.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device interfaces", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deviceIPAddresses(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	items, err := s.inventory.IPAddresses.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device IP addresses", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deviceServices(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	items, err := s.inventory.Services.ListByDevice(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device services", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deviceRelationships(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	deviceID, ok := deviceIDFromRequest(w, r)
	if !ok {
		return
	}
	items, err := s.inventory.Relationships.ListByEntity(r.Context(), deviceID)
	if err != nil {
		s.inventoryFailure(w, "list device relationships", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func deviceIDFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("deviceID")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_device_id", "Device ID must be a UUID.")
		return "", false
	}
	return id, true
}

func isUUID(value string) bool { return uuidPattern.MatchString(value) }

func integerQuery(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

func (s *Server) inventoryFailure(w http.ResponseWriter, operation string, err error) {
	s.logger.Error("inventory query failed", slog.String("operation", operation), slog.Any("error", err))
	writeError(w, http.StatusInternalServerError, "inventory_unavailable", "Inventory data could not be loaded.")
}
