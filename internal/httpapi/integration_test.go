//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/integrations/ntopng"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/platform/database"
	"github.com/nerve-labs/nerve/internal/traffic"
)

func TestAPIFlow(t *testing.T) {
	databaseURL := os.Getenv("NERVE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NERVE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer adminPool.Close()
	if err := adminPool.Ping(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	schema := fmt.Sprintf("nerve_http_it_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
	}()

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("open isolated integration schema: %v", err)
	}
	defer pool.Close()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations"))
	if err := database.Migrate(ctx, pool, migrationsDir); err != nil {
		t.Fatalf("migrate integration schema: %v", err)
	}

	const agentToken = "ci-discovery-agent-token"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpapi.New(pool, config.Config{
		AllowedOrigin:        "http://localhost:3000",
		SessionTTL:           24 * time.Hour,
		DiscoveryAgentToken:  agentToken,
		DiscoveryLegacyToken: true,
	}, logger, "ci").Handler())
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}

	bootstrap := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/bootstrap", `{"email":"ci-admin@example.invalid","password":"this-is-a-ci-password"}`, nil)
	if bootstrap.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, want 201: %s", bootstrap.StatusCode, readBody(bootstrap))
	}
	_ = bootstrap.Body.Close()

	session := doJSON(t, client, http.MethodGet, server.URL+"/api/v1/auth/session", "", nil)
	if session.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d, want 200: %s", session.StatusCode, readBody(session))
	}
	var sessionBody struct {
		Authenticated bool `json:"authenticated"`
	}
	decodeResponse(t, session, &sessionBody)
	if !sessionBody.Authenticated {
		t.Fatal("bootstrap session is not authenticated")
	}

	// Authenticated traffic diagnostics must preserve distinct machine-readable states.
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ntopng.ErrNotConfigured, 503, "traffic_source_not_configured"},
		{ntopng.ErrAuthentication, 502, "traffic_source_authentication_failed"},
		{ntopng.ErrUnavailable, 502, "traffic_source_unreachable"},
		{ntopng.ErrInvalidResponse, 502, "traffic_source_invalid_response"},
		{ntopng.ErrInvalidConfiguration, 502, "traffic_source_invalid_configuration"},
		{nil, 200, ""},
	} {
		diagnostic := httptest.NewServer(httpapi.NewWithTraffic(pool, config.Config{SessionTTL: 24 * time.Hour, TrafficCaptureMode: "linux-interface", NtopngInterfaceID: "7", NtopngURL: "http://private-sensor.invalid", NtopngUsername: "private-reader", NtopngPassword: "private-test-secret"}, logger, "test", trafficTestSource{err: tc.err}).Handler())
		response := doJSON(t, client, http.MethodGet, diagnostic.URL+"/api/v1/traffic/live", "", nil)
		body := readBody(response)
		if response.StatusCode != tc.status || (tc.code != "" && !strings.Contains(body, tc.code)) {
			t.Fatalf("traffic diagnostic: status=%d body=%s", response.StatusCode, body)
		}
		if !strings.Contains(body, `"mode":"linux-interface"`) || !strings.Contains(body, `"interfaceId":"7"`) || !strings.Contains(body, `"visibility"`) {
			t.Fatal("capture diagnostics missing")
		}
		if strings.Contains(body, "private-") {
			t.Fatal("sensor credentials or address leaked")
		}
		anonymous, err := http.Get(diagnostic.URL + "/api/v1/traffic/live")
		if err != nil {
			t.Fatal(err)
		}
		_ = anonymous.Body.Close()
		if anonymous.StatusCode != 401 {
			t.Fatal("traffic endpoint must require a session")
		}
		diagnostic.Close()
	}
	targetResponse := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/target", `{"name":"CI network","cidr":"192.168.178.0/24"}`, nil)
	if targetResponse.StatusCode != http.StatusOK {
		t.Fatalf("save target status = %d, want 200: %s", targetResponse.StatusCode, readBody(targetResponse))
	}
	var target struct {
		ID string `json:"id"`
	}
	decodeResponse(t, targetResponse, &target)
	if target.ID == "" {
		t.Fatal("saved discovery target has no id")
	}

	scanResponse := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/scans", fmt.Sprintf(`{"targetId":%q}`, target.ID), nil)
	if scanResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("queue scan status = %d, want 202: %s", scanResponse.StatusCode, readBody(scanResponse))
	}
	var scan struct {
		ID string `json:"id"`
	}
	decodeResponse(t, scanResponse, &scan)

	agentHeaders := map[string]string{"Authorization": "Bearer " + agentToken}
	heartbeat := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/agent/heartbeat", `{"id":"ci-agent","hostname":"ci-runner","platform":"ci","version":"test","capabilities":["report"]}`, agentHeaders)
	if heartbeat.StatusCode != http.StatusNoContent {
		t.Fatalf("agent heartbeat status = %d, want 204: %s", heartbeat.StatusCode, readBody(heartbeat))
	}
	_ = heartbeat.Body.Close()

	claimHeaders := map[string]string{"Authorization": "Bearer " + agentToken, "X-NERVE-Agent-ID": "ci-agent"}
	claim := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/agent/jobs/claim", `{}`, claimHeaders)
	if claim.StatusCode != http.StatusOK {
		t.Fatalf("claim scan status = %d, want 200: %s", claim.StatusCode, readBody(claim))
	}
	var job struct {
		ID   string `json:"id"`
		CIDR string `json:"cidr"`
	}
	decodeResponse(t, claim, &job)
	if job.ID != scan.ID || job.CIDR != "192.168.178.0/24" {
		t.Fatalf("unexpected claimed job: %+v", job)
	}

	reportBody := `{"observations":[{"address":"192.168.178.50","reachable":true,"hostname":"ci-device","mac":"aa:bb:cc:dd:ee:ff","method":"ci-smoke"}],"errors":[]}`
	report := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/agent/jobs/"+job.ID+"/report", reportBody, claimHeaders)
	if report.StatusCode != http.StatusNoContent {
		t.Fatalf("report scan status = %d, want 204: %s", report.StatusCode, readBody(report))
	}
	_ = report.Body.Close()

	complete := doJSON(t, client, http.MethodPost, server.URL+"/api/v1/discovery/agent/jobs/"+job.ID+"/complete", `{"addressesTotal":254,"status":"succeeded"}`, claimHeaders)
	if complete.StatusCode != http.StatusNoContent {
		t.Fatalf("complete scan status = %d, want 204: %s", complete.StatusCode, readBody(complete))
	}
	_ = complete.Body.Close()

	devicesResponse := doJSON(t, client, http.MethodGet, server.URL+"/api/v1/devices", "", nil)
	if devicesResponse.StatusCode != http.StatusOK {
		t.Fatalf("devices status = %d, want 200: %s", devicesResponse.StatusCode, readBody(devicesResponse))
	}
	var devices inventory.DeviceList
	decodeResponse(t, devicesResponse, &devices)
	if devices.Total != 1 || len(devices.Items) != 1 {
		t.Fatalf("device total = %d, returned = %d, want exactly one", devices.Total, len(devices.Items))
	}
	device := devices.Items[0]
	if device.Hostname != "ci-device" || device.PrimaryIPAddress != "192.168.178.50" || device.PrimaryMAC != "aa:bb:cc:dd:ee:ff" || device.Status != "online" {
		t.Fatalf("unexpected discovered device: %+v", device)
	}

	detailResponse := doJSON(t, client, http.MethodGet, server.URL+"/api/v1/devices/"+device.ID, "", nil)
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("device detail status = %d, want 200: %s", detailResponse.StatusCode, readBody(detailResponse))
	}
	var detail inventory.DeviceDetail
	decodeResponse(t, detailResponse, &detail)
	if detail.Device.ID != device.ID || len(detail.IPAddresses) == 0 || detail.IPAddresses[0].Address != "192.168.178.50" {
		t.Fatalf("unexpected device detail: %+v", detail)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(response *http.Response) string {
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return string(body)
}

type trafficTestSource struct{ err error }

func (s trafficTestSource) Live(context.Context) (traffic.Snapshot, error) {
	return traffic.Snapshot{Source: "ntopng", Flows: []traffic.Flow{}}, s.err
}
