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
	if s.traffic == nil {
		writeError(w, 503, "traffic_source_not_configured", "Traffic source not configured")
		return
	}
	snapshot, err := s.traffic.Live(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ntopng.ErrNotConfigured):
			writeError(w, 503, "traffic_source_not_configured", "Traffic source not configured")
		case errors.Is(err, ntopng.ErrAuthentication):
			writeError(w, 502, "traffic_source_authentication_failed", "Authentication failed")
		case errors.Is(err, ntopng.ErrInvalidConfiguration):
			writeError(w, 502, "traffic_source_invalid_configuration", "Invalid ntopng configuration")
		case errors.Is(err, ntopng.ErrInvalidResponse):
			writeError(w, 502, "traffic_source_invalid_response", "Invalid response from ntopng")
		default:
			writeError(w, 502, "traffic_source_unreachable", "Traffic source unreachable")
		}
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

var _ traffic.Source = (*ntopng.Client)(nil)
