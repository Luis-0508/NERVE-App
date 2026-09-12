package httpapi

import (
	"errors"
	"github.com/nerve-labs/nerve/internal/integrations/ntopng"
	"github.com/nerve-labs/nerve/internal/traffic"
	"net/http"
	"net/netip"
	"strings"
)

func parseTrafficCIDRs(value string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(raw)); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}
func (s *Server) liveTraffic(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	capture := traffic.CaptureInfo(s.cfg.TrafficCaptureMode, s.cfg.NtopngURL, s.cfg.NtopngInterfaceID)
	writeTrafficError := func(w http.ResponseWriter, status int, code, message string) {
		if code == "traffic_source_not_configured" && capture.Mode == "sensor-required" {
			message = "Capture sensor required"
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "capture": capture})
	}
	if s.traffic == nil {
		writeTrafficError(w, 503, "traffic_source_not_configured", "Traffic source not configured")
		return
	}
	snapshot, err := s.traffic.Live(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ntopng.ErrNotConfigured):
			writeTrafficError(w, 503, "traffic_source_not_configured", "Traffic source not configured")
		case errors.Is(err, ntopng.ErrAuthentication):
			writeTrafficError(w, 502, "traffic_source_authentication_failed", "Authentication failed")
		case errors.Is(err, ntopng.ErrInvalidConfiguration):
			writeTrafficError(w, 502, "traffic_source_invalid_configuration", "Invalid ntopng configuration")
		case errors.Is(err, ntopng.ErrInvalidResponse):
			writeTrafficError(w, 502, "traffic_source_invalid_response", "Invalid response from ntopng")
		default:
			writeTrafficError(w, 502, "traffic_source_unreachable", "Traffic source unreachable")
		}
		return
	}
	if snapshot.Capture != nil {
		capture.InterfaceID = snapshot.Capture.InterfaceID
		capture.InterfaceName = snapshot.Capture.InterfaceName
	}
	snapshot.Capture = &capture
	writeJSON(w, http.StatusOK, snapshot)
}

var _ traffic.Source = (*ntopng.Client)(nil)
