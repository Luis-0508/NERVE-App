package ntopng

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActiveFlowParsingAndCredentialsStayInRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "operator" || pass != "secret" {
			t.Fatal("expected Basic Auth only on server request")
		}
		if r.URL.Query().Get("ifid") != "3" {
			t.Fatalf("unexpected interface %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/lua/rest/v2/get/host/active.lua" {
			_, _ = w.Write([]byte(`{"rc":0,"rsp":{"data":[{"ip":"1.1.1.1","country":"DE"}]}}`))
			return
		}
		if r.URL.Path != "/lua/rest/v2/get/flow/active.lua" {
			t.Fatalf("unexpected ntopng request %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"rc":0,"rsp":{"data":[{"hash_id":"x","client":{"ip":"192.168.1.8","port":52000},"server":{"ip":"1.1.1.1","port":443},"first_seen":10,"last_seen":11,"bytes":42,"protocol":{"l4":"TCP","l7":"TLS.Cloudflare"},"thpt":{"bps":8000}}]}}`))
	}))
	defer server.Close()
	snapshot, err := New(Config{URL: server.URL, Username: "operator", Password: "secret", InterfaceID: "3", Timeout: time.Second}).Live(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Flows) != 1 {
		t.Fatalf("expected one mapped flow: %#v", snapshot)
	}
	flow := snapshot.Flows[0]
	if flow.Direction != "outbound" || flow.Protocol != "TCP" || flow.Application != "TLS.Cloudflare" || flow.BytesPerSecond != 1000 || flow.RemoteCountryName != "Germany" {
		t.Fatalf("unexpected parsed flow: %#v", flow)
	}
}

func TestRejectsInvalidAndAuthenticationResponses(t *testing.T) {
	if _, err := New(Config{}).Live(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	if _, err := New(Config{URL: server.URL, Username: "a", Password: "b", Timeout: time.Second}).Live(context.Background()); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestTimeoutIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(100 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	if _, err := New(Config{URL: server.URL, Username: "a", Password: "b", Timeout: 5 * time.Millisecond}).Live(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable timeout, got %v", err)
	}
}

func TestResponseDiagnostics(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		location   string
		want       error
	}{
		{"login redirect", "", 302, "/lua/login.lua", ErrAuthentication},
		{"unauthorized", "", 401, "", ErrAuthentication},
		{"forbidden", "", 403, "", ErrAuthentication},
		{"missing endpoint", "", 404, "", ErrInvalidResponse},
		{"upstream down", "", 503, "", ErrUnavailable},
		{"html", "<html>login</html>", 200, "", ErrInvalidResponse},
		{"empty object", "{}", 200, "", ErrInvalidResponse},
		{"missing rc", `{"rsp":{"data":[]}}`, 200, "", ErrInvalidResponse},
		{"missing data", `{"rc":0,"rsp":{}}`, 200, "", ErrInvalidResponse},
		{"null rows", `{"rc":0,"rsp":{"data":null}}`, 200, "", ErrInvalidResponse},
		{"invalid interface", `{"rc":-2}`, 200, "", ErrInvalidResponse},
		{"bad row", `{"rc":0,"rsp":{"data":[{}]}}`, 200, "", ErrInvalidResponse},
		{"empty array", `{"rc":0,"rsp":{"data":[]}}`, 200, "", nil},
		{"empty lua table", `{"rc":0,"rsp":{"data":{}}}`, 200, "", nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.location != "" {
					w.Header().Set("Location", tt.location)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := New(Config{URL: server.URL, Username: "a", Password: "b"}).Live(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}
func TestPaginationInboundPortsPrecisionAndCache(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/ntop/lua/rest/v2/get/flow/active.lua" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("perPage") != "500" {
			t.Error("page size not set")
		}
		id, port := "one", "443"
		if r.URL.Query().Get("currentPage") == "2" {
			id, port = "two", "8443"
		}
		_, _ = w.Write([]byte(`{"rc":0,"rsp":{"totalRows":2,"data":[{"hash_id":"` + id + `","client":{"ip":"8.8.8.8","port":12345,"country":"US"},"server":{"ip":"10.0.0.2","port":` + port + `,"country":"DE"},"protocol":{"l4":"TCP","l7":"TLS"},"bytes":9007199254740993,"thpt":{"bps":800}}]}}`))
	}))
	defer server.Close()
	client := New(Config{URL: server.URL + "/ntop/", Username: "a", Password: "b"})
	snapshot, err := client.Live(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || snapshot.Summary.ActiveFlows != 2 || snapshot.Truncated {
		t.Fatalf("pagination: calls=%d snapshot=%+v", calls, snapshot)
	}
	f := snapshot.Flows[0]
	if f.Direction != "inbound" || f.Bytes != 18014398509481986 || f.BytesPerSecond != 200 || f.RemotePort != 12345 || len(f.LocalPorts) != 2 || f.FlowCount != 2 {
		t.Fatalf("bad normalized flow: %+v", f)
	}
	again, err := client.Live(context.Background())
	if err != nil || calls != 2 || again.Flows[0].ID != f.ID {
		t.Fatal("cache or stable IDs failed")
	}
}
func TestConfigurationAndInvalidByteCounts(t *testing.T) {
	for _, url := range []string{"file:///private", "http://user:pass@localhost", "http://localhost?secret=x"} {
		_, err := New(Config{URL: url, Username: "u", Password: "p"}).Live(context.Background())
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid URL classified as %v", err)
		}
	}
	for _, raw := range []string{"null", "-1", "1.5", `{"total":-2}`, `"10"`} {
		if _, err := parseBytes([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
