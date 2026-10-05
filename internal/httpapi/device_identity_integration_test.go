//go:build integration

package httpapi_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

func TestDeviceMetadataAndTrustAPI(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_device_meta")
	testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	testsupport.CreateUser(t, pool, "viewer@example.invalid", "integration-password", "read_only")
	deviceID := testsupport.CreateDevice(t, pool, "desktop-abc123", "02:00:00:00:00:51", "192.0.2.51")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour, CoreSigningKey: testsupport.CoreSigningKeyHex},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")
	viewer := login(t, server.URL, "viewer@example.invalid", "integration-password")
	metadataURL := server.URL + "/api/v1/devices/" + deviceID + "/metadata"
	trustURL := server.URL + "/api/v1/devices/" + deviceID + "/trust"

	status := func(client *http.Client, url, body string) (int, string) {
		response := doJSON(t, client, http.MethodPost, url, body, nil)
		return response.StatusCode, readBody(response)
	}

	if code, _ := status(viewer, metadataURL, `{"displayName":"x"}`); code != http.StatusForbidden {
		t.Fatalf("viewer metadata = %d", code)
	}
	code, body := status(technician, metadataURL, `{"displayName":"Luis-PC","tags":["Office"],"classification":"workstation","notes":"desk"}`)
	if code != http.StatusOK || !strings.Contains(body, `"name":"Luis-PC"`) || !strings.Contains(body, `"tags":["office"]`) ||
		!strings.Contains(body, `"discoveredName":"desktop-abc123"`) || !strings.Contains(body, `"classificationOverridden":true`) {
		t.Fatalf("technician metadata = %d: %s", code, body)
	}
	if code, _ := status(technician, trustURL, `{"state":"trusted"}`); code != http.StatusForbidden {
		t.Fatalf("technician trust = %d", code)
	}
	if code, body := status(admin, trustURL, `{"state":"trusted","reason":"verified on site"}`); code != http.StatusOK || !strings.Contains(body, `"trustState":"trusted"`) || !strings.Contains(body, `"trustChangedBy":"admin@example.invalid"`) {
		t.Fatalf("admin trust = %d: %s", code, body)
	}
	if code, _ := status(admin, trustURL, `{"state":"promoted"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid trust = %d", code)
	}
	if code, _ := status(admin, metadataURL, `{"displayName":"x","owner":{"a":1}}`); code != http.StatusBadRequest {
		t.Fatalf("non-string field = %d", code)
	}
	if code, _ := status(admin, metadataURL, `{"secret":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", code)
	}
	// null clears the manual name; the discovered name becomes effective again.
	if code, body := status(admin, metadataURL, `{"displayName":null}`); code != http.StatusOK || !strings.Contains(body, `"name":"desktop-abc123"`) {
		t.Fatalf("clear name = %d: %s", code, body)
	}

	list := readBody(doJSON(t, viewer, http.MethodGet, server.URL+"/api/v1/devices?tag=office&trust=trusted", "", nil))
	if !strings.Contains(list, deviceID) || !strings.Contains(list, `"total":1`) {
		t.Fatalf("filtered list: %s", list)
	}
	detail := readBody(doJSON(t, viewer, http.MethodGet, server.URL+"/api/v1/devices/"+deviceID, "", nil))
	if !strings.Contains(detail, `"facts":[`) || !strings.Contains(detail, `"notes":"desk"`) {
		t.Fatalf("detail: %s", detail)
	}
	auditBody := readBody(doJSON(t, admin, http.MethodGet, server.URL+"/api/v1/audit?targetType=device&targetId="+deviceID, "", nil))
	for _, expected := range []string{`"action":"device.metadata.changed"`, `"action":"device.trust.changed"`, `"actorEmail":"tech@example.invalid"`} {
		if !strings.Contains(auditBody, expected) {
			t.Fatalf("audit lacks %s: %s", expected, auditBody)
		}
	}
	if strings.Contains(auditBody, `"desk"`) {
		t.Fatal("notes content leaked into the audit log")
	}
}
