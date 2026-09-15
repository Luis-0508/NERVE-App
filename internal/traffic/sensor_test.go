package traffic

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestNativeSensor(t *testing.T) {
	now := time.Now().UTC()
	token := strings.Repeat("a", 64)
	s := NewSensor(token, func(netip.Addr) string { return "US" })
	s.now = func() time.Time { return now }
	if _, err := s.Live(context.Background()); err != ErrSensorOffline {
		t.Fatal(err)
	}
	report := SensorReport{Version: 1, GeneratedAt: now, Status: "connected", InterfaceID: "nic", InterfaceName: "Ethernet", LocalAddresses: []string{"192.168.1.2"}, Flows: []Flow{{LocalIP: "192.168.1.2", RemoteIP: "1.1.1.1", LocalPort: 50000, RemotePort: 443, Protocol: "TCP", Direction: DirectionInbound, Bytes: 100, BytesPerSecond: 50, FirstSeenAt: now, LastSeenAt: now}}}
	send := func(report SensorReport, key, nonce string, stamp time.Time) int {
		b, _ := json.Marshal(report)
		r := httptest.NewRequest("POST", "/v1/snapshot", bytes.NewReader(b))
		ts := fmt.Sprint(stamp.Unix())
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(ts + "\n" + nonce + "\n"))
		mac.Write(b)
		r.Header.Set("X-Nerve-Time", ts)
		r.Header.Set("X-Nerve-Nonce", nonce)
		r.Header.Set("X-Nerve-Signature", hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("credential leak")
		}
		return w.Code
	}
	nonce := strings.Repeat("1", 32)
	if got := send(report, "wrong", nonce, now); got != 401 {
		t.Fatal(got)
	}
	if got := send(report, token, nonce, now.Add(-time.Minute)); got != 401 {
		t.Fatal(got)
	}
	if got := send(report, token, nonce, now); got != 204 {
		t.Fatal(got)
	}
	if got := send(report, token, nonce, now); got != 409 {
		t.Fatal(got)
	}
	snapshot, err := s.Live(context.Background())
	if err != nil || len(snapshot.Flows) != 1 || snapshot.Flows[0].Bytes != 100 || snapshot.Flows[0].RemoteLatitude == nil || snapshot.Flows[0].Application != "Unknown" {
		t.Fatalf("invalid normalization %+v %v", snapshot, err)
	}
	id := snapshot.Flows[0].ID
	report.Flows[0].Bytes = 200
	if got := send(report, token, strings.Repeat("2", 32), now); got != 204 {
		t.Fatal(got)
	}
	snapshot, _ = s.Live(context.Background())
	if snapshot.Flows[0].ID != id || snapshot.Flows[0].Bytes != 200 {
		t.Fatal("unstable update")
	}
	now = now.Add(11 * time.Second)
	if _, err := s.Live(context.Background()); err != ErrSensorOffline {
		t.Fatal("stale sensor accepted")
	}
	report.GeneratedAt = now
	report.Status = "capture_permission_denied"
	report.Flows = nil
	if got := send(report, token, strings.Repeat("3", 32), now); got != 204 {
		t.Fatal(got)
	}
	if _, err := s.Live(context.Background()); err != ErrSensorPermission {
		t.Fatal(err)
	}
	report.Status = "connected"
	if got := send(report, token, strings.Repeat("4", 32), now); got != 204 {
		t.Fatal(got)
	}
	snapshot, err = s.Live(context.Background())
	if err != nil || snapshot.Summary.ActiveFlows != 0 {
		t.Fatal("no-flow recovery failed")
	}
	report.Flows = []Flow{{LocalIP: "10.0.0.3", RemoteIP: "1.1.1.1", Protocol: "TCP", Direction: DirectionOutbound, FirstSeenAt: now, LastSeenAt: now}}
	if got := send(report, token, strings.Repeat("5", 32), now); got != 400 {
		t.Fatal("unowned local address accepted")
	}
	r := httptest.NewRequest("POST", "/v1/snapshot", strings.NewReader("{}"))
	r.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("browser ingress accepted")
	}
}
