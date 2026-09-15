package traffic

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

var ErrSensorOffline = errors.New("Windows traffic sensor has not reported within 10 seconds")
var ErrSensorPermission = errors.New("Windows capture permission denied")
var ErrSensorBackend = errors.New("Windows capture backend unavailable")

// SensorStore is a bounded, memory-only TrafficSource. Its handler belongs to a
// separate host-loopback listener, never to the web reverse proxy.
type SensorStore struct {
	mu         sync.Mutex
	token      string
	now        func() time.Time
	received   time.Time
	status     string
	snapshot   Snapshot
	nonces     map[string]time.Time
	country    func(netip.Addr) string
	localCIDRs []netip.Prefix
}
type SensorReport struct {
	Version        int       `json:"version"`
	GeneratedAt    time.Time `json:"generatedAt"`
	Status         string    `json:"status"`
	InterfaceID    string    `json:"interfaceId"`
	InterfaceName  string    `json:"interfaceName"`
	LocalAddresses []string  `json:"localAddresses"`
	Flows          []Flow    `json:"flows"`
	Truncated      bool      `json:"truncated"`
	EventsLost     int64     `json:"eventsLost"`
	BackendStage   string    `json:"backendStage"`
	BackendError   int       `json:"backendError"`
}

func NewSensor(token string, country func(netip.Addr) string, localCIDRs ...netip.Prefix) *SensorStore {
	return &SensorStore{token: token, now: time.Now, nonces: map[string]time.Time{}, country: country, localCIDRs: localCIDRs}
}
func (s *SensorStore) Live(context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.received.IsZero() || s.now().Sub(s.received) > 10*time.Second {
		return s.snapshot, ErrSensorOffline
	}
	switch s.status {
	case "capture_permission_denied":
		return s.snapshot, ErrSensorPermission
	case "capture_backend_unavailable":
		return s.snapshot, ErrSensorBackend
	}
	return s.snapshot, nil
}
func (s *SensorStore) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/snapshot", s.ingest)
	// Sanitized readiness only. No flow data or secrets on this listener's GET route.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := s.Live(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		status := "connected"
		if err != nil {
			status = err.Error()
			w.WriteHeader(503)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "capture": snapshot.Capture, "activeFlows": snapshot.Summary.ActiveFlows})
	})
	return mux
}
func (s *SensorStore) ingest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Browser-originated requests are never sensor reports, even from same-site pages.
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" || len(s.token) < 32 {
		http.Error(w, "sensor authentication required", 401)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "report too large", 413)
		return
	}
	stamp, nonce := r.Header.Get("X-Nerve-Time"), r.Header.Get("X-Nerve-Nonce")
	unix, err := strconv.ParseInt(stamp, 10, 64)
	now := s.now()
	signature, e := hex.DecodeString(r.Header.Get("X-Nerve-Signature"))
	nonceBytes, ne := hex.DecodeString(nonce)
	mac := hmac.New(sha256.New, []byte(s.token))
	mac.Write([]byte(stamp + "\n" + nonce + "\n"))
	mac.Write(body)
	if err != nil || e != nil || ne != nil || len(nonceBytes) != 16 || now.Sub(time.Unix(unix, 0)).Abs() > 30*time.Second || !hmac.Equal(signature, mac.Sum(nil)) {
		http.Error(w, "sensor authentication failed", 401)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, t := range s.nonces {
		if now.Sub(t) > time.Minute {
			delete(s.nonces, n)
		}
	}
	if _, exists := s.nonces[nonce]; exists {
		http.Error(w, "replayed report", 409)
		return
	}
	if len(s.nonces) >= 120 {
		http.Error(w, "report rate exceeded", 429)
		return
	}
	s.nonces[nonce] = now
	var report SensorReport
	if json.Unmarshal(body, &report) != nil || report.Version != 1 || len(report.Flows) > 5000 || len(report.LocalAddresses) > 64 || len(report.InterfaceName) > 256 || len(report.InterfaceID) > 128 || now.Sub(report.GeneratedAt).Abs() > 30*time.Second {
		http.Error(w, "invalid sensor report", 400)
		return
	}
	switch report.Status {
	case "connected", "capture_permission_denied", "capture_backend_unavailable":
	default:
		http.Error(w, "invalid capture status", 400)
		return
	}
	locals := map[netip.Addr]bool{}
	for _, raw := range report.LocalAddresses {
		if ip, e := netip.ParseAddr(raw); e == nil {
			locals[ip.Unmap()] = true
		}
	}
	if report.Status == "connected" && (report.InterfaceID == "" || len(locals) == 0) {
		http.Error(w, "missing capture interface", 400)
		return
	}
	flows := make([]Flow, 0, len(report.Flows))
	for _, f := range report.Flows {
		local, e1 := netip.ParseAddr(f.LocalIP)
		remote, e2 := netip.ParseAddr(f.RemoteIP)
		if e1 != nil || e2 != nil || !locals[local.Unmap()] || (f.Protocol != "TCP" && f.Protocol != "UDP") || (f.Direction != DirectionInbound && f.Direction != DirectionOutbound) || f.BytesPerSecond < 0 || math.IsNaN(f.BytesPerSecond) || math.IsInf(f.BytesPerSecond, 0) || f.FirstSeenAt.IsZero() || f.LastSeenAt.Before(f.FirstSeenAt) || f.LastSeenAt.After(now.Add(30*time.Second)) {
			http.Error(w, "invalid flow", 400)
			return
		}
		remote = remote.Unmap()
		if !remote.IsGlobalUnicast() || IsLocalIP(remote.String(), s.localCIDRs) || locals[remote] {
			continue
		}
		// Never trust client-supplied map coordinates, IDs, totals or application guesses.
		clean := Flow{LocalIP: local.Unmap().String(), RemoteIP: remote.String(), LocalPort: f.LocalPort, RemotePort: f.RemotePort, Protocol: f.Protocol, Direction: f.Direction, Bytes: f.Bytes, BytesPerSecond: f.BytesPerSecond, FirstSeenAt: f.FirstSeenAt, LastSeenAt: f.LastSeenAt, Application: "Unknown"}
		if s.country != nil {
			clean.RemoteCountryCode = s.country(remote)
			SetCountryLocation(&clean)
		}
		flows = append(flows, clean)
	}
	grouped, summary := Aggregate(flows, 500)
	summary.ActiveFlows = len(flows)
	capture := CaptureInfo("windows-native", "", report.InterfaceID)
	capture.InterfaceName = report.InterfaceName
	capture.EventsLost = report.EventsLost
	switch report.BackendStage {
	case "interface_selection", "etw_session", "etw_processing":
		capture.BackendStage = report.BackendStage
		capture.BackendError = report.BackendError
	}
	s.snapshot = Snapshot{Capture: &capture, GeneratedAt: report.GeneratedAt, Source: "windows-native", Flows: grouped, Summary: summary, SampledFlows: len(flows), SourceTotalFlows: len(flows), Truncated: report.Truncated || summary.AggregatedFlows > 500 || report.EventsLost > 0}
	s.received = now
	s.status = report.Status
	w.WriteHeader(http.StatusNoContent)
}
