package meshcentral

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeControl is a scripted /control.ashx. reply maps each request to the
// messages the server sends back; unsolicited messages go out first.
type fakeControl struct {
	t           *testing.T
	unsolicited []string
	reply       func(request map[string]any) []string
}

func (f fakeControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/control.ashx" {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		f.t.Errorf("accept: %v", err)
		return
	}
	defer conn.CloseNow() //nolint:errcheck
	ctx := r.Context()
	for _, message := range f.unsolicited {
		if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
			return
		}
	}
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var request map[string]any
		if err := json.Unmarshal(payload, &request); err != nil {
			f.t.Errorf("client sent malformed JSON: %s", payload)
			return
		}
		for _, message := range f.reply(request) {
			if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
				return
			}
		}
	}
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user)) + "," + base64.StdEncoding.EncodeToString([]byte(pass))
}

func TestNodesListsNodesWithNetworkFacts(t *testing.T) {
	var gotAuth string
	fake := fakeControl{t: t,
		unsolicited: []string{`{"action":"serverinfo","serverinfo":{}}`, `{"action":"userinfo","userinfo":{"name":"nerve"}}`},
		reply: func(request map[string]any) []string {
			switch request["action"] {
			case "nodes":
				return []string{
					`{"action":"event","event":{"action":"nodeconnect"}}`,
					`{"action":"nodes","responseid":"nerve","nodes":{"mesh//g1":[{"_id":"node//Ab$c@1","name":"Office PC","rname":"OFFICE-PC","osdesc":"Windows 11","conn":1}],"mesh//g2":[{"_id":"node//xyz","name":"nas","conn":0},{"_id":"node//disp","name":"Lab NAS"}]}}`,
				}
			case "getnetworkinfo":
				if request["nodeid"] == "node//Ab$c@1" {
					return []string{`{"action":"getnetworkinfo","nodeid":"node//Ab$c@1","netif2":{"Ethernet":[{"mac":"AA-BB-CC-DD-EE-FF","address":"192.168.1.20","family":"IPv4"},{"mac":"00:00:00:00:00:00","address":"127.0.0.1"}]}}`}
				}
				return []string{fmt.Sprintf(`{"action":"getnetworkinfo","nodeid":%q,"noinfo":true,"result":"Invalid device id"}`, request["nodeid"])}
			case "lastconnect":
				if request["nodeid"] == "node//Ab$c@1" {
					return []string{`{"action":"lastconnect","nodeid":"node//Ab$c@1","time":1761700000000,"addr":"203.0.113.9"}`}
				}
				return []string{fmt.Sprintf(`{"action":"lastconnect","nodeid":%q,"noinfo":true,"result":"No data"}`, request["nodeid"])}
			}
			t.Errorf("unexpected request %v", request)
			return nil
		}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-meshauth")
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()

	nodes, err := New(Config{URL: server.URL + "/", Username: "nerve-svc", Password: "s3cret", Timeout: 5 * time.Second}).Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != basicAuth("nerve-svc", "s3cret") {
		t.Fatalf("x-meshauth = %q", gotAuth)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected three nodes, got %#v", nodes)
	}
	office := nodes[0]
	if office.ID != "node//Ab$c@1" || office.MeshID != "mesh//g1" || office.Hostname != "office-pc" || !office.AgentConnected() {
		t.Fatalf("unexpected node %#v", office)
	}
	if len(office.MACs) != 1 || office.MACs[0] != "aa:bb:cc:dd:ee:ff" || len(office.Addresses) != 2 {
		t.Fatalf("unexpected network facts %#v", office)
	}
	if office.LastConnectAt == nil || office.LastConnectAt.UnixMilli() != 1761700000000 {
		t.Fatalf("unexpected last connect %#v", office.LastConnectAt)
	}
	// A display name with spaces is not hostname evidence.
	if display := nodes[1]; display.ID != "node//disp" || display.Hostname != "" || display.Name != "Lab NAS" {
		t.Fatalf("display name used as hostname %#v", display)
	}
	if nas := nodes[2]; nas.AgentConnected() || len(nas.MACs) != 0 || nas.LastConnectAt != nil || nas.Hostname != "nas" {
		t.Fatalf("unexpected node without details %#v", nas)
	}
}

func TestNodesAuthenticationFailure(t *testing.T) {
	for _, tc := range []struct{ close, want string }{
		{`{"action":"close","cause":"noauth","msg":"noauth-2"}`, "invalid login"},
		{`{"action":"close","cause":"noauth","msg":"tokenrequired"}`, "second factor"},
		{`{"action":"close","cause":"banned"}`, "too many failed logins"},
	} {
		server := httptest.NewServer(fakeControl{t: t, unsolicited: []string{tc.close}, reply: func(map[string]any) []string { return nil }})
		_, err := New(Config{URL: server.URL, Username: "nerve-svc", Password: "wrong", Timeout: 5 * time.Second}).Nodes(context.Background())
		server.Close()
		if !errors.Is(err, ErrAuthentication) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("close %s: expected authentication error with %q, got %v", tc.close, tc.want, err)
		}
		if strings.Contains(err.Error(), "wrong") {
			t.Fatal("the error leaks the password")
		}
	}
}

func TestNodesHandshakeRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	_, err := New(Config{URL: server.URL, Username: "nerve-svc", Password: "s3cret"}).Nodes(context.Background())
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestNodesUnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL + "/?key=loginkey123"
	server.Close()
	_, err := New(Config{URL: url, Username: "nerve-svc", Password: "s3cret", Timeout: 2 * time.Second}).Nodes(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if strings.Contains(err.Error(), "loginkey123") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("the error leaks a credential: %v", err)
	}
}

func TestNodesMalformedMessages(t *testing.T) {
	for name, reply := range map[string]string{
		"not json":        `{"action":`,
		"bad node id":     `{"action":"nodes","responseid":"nerve","nodes":{"mesh//g":[{"_id":"../../etc","name":"x"}]}}`,
		"wrong node type": `{"action":"nodes","responseid":"nerve","nodes":{"mesh//g":"oops"}}`,
		"refused":         `{"action":"nodes","responseid":"nerve","result":"Invalid group id"}`,
	} {
		server := httptest.NewServer(fakeControl{t: t, reply: func(map[string]any) []string { return []string{reply} }})
		_, err := New(Config{URL: server.URL, Username: "nerve-svc", Password: "s3cret", Timeout: 5 * time.Second}).Nodes(context.Background())
		server.Close()
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("%s: expected invalid response, got %v", name, err)
		}
	}
}

func TestNodesNotConfigured(t *testing.T) {
	if _, err := New(Config{URL: "https://mesh.example"}).Nodes(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected not configured, got %v", err)
	}
}

func TestControlURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://mesh.example":              "wss://mesh.example/control.ashx",
		"https://mesh.example/tenant/":      "wss://mesh.example/tenant/control.ashx",
		"http://meshcentral:4430?key=abc&x": "ws://meshcentral:4430/control.ashx?key=abc",
	} {
		got, err := ControlURL(input)
		if err != nil || got != want {
			t.Fatalf("ControlURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"ftp://mesh.example", "https://user:pass@mesh.example", "mesh.example", ""} {
		if _, err := ControlURL(input); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("ControlURL(%q) accepted", input)
		}
	}
}

func TestValidNodeIDAndMAC(t *testing.T) {
	for id, want := range map[string]bool{"node//abc$@": true, "node/tenant/abc": true, "node//": false, "mesh//abc": false, "node//a/b": false, "node//a b": false} {
		if ValidNodeID(id) != want {
			t.Fatalf("ValidNodeID(%q) != %v", id, want)
		}
	}
	for input, want := range map[string]string{"AA-BB-CC-DD-EE-FF": "aa:bb:cc:dd:ee:ff", "00:00:00:00:00:00": "", "aa:bb": "", "zz:bb:cc:dd:ee:ff": ""} {
		if got := NormalizeMAC(input); got != want {
			t.Fatalf("NormalizeMAC(%q) = %q", input, got)
		}
	}
}

func TestNodesVerifiesTLSAgainstConfiguredRoots(t *testing.T) {
	server := httptest.NewTLSServer(fakeControl{t: t, reply: func(map[string]any) []string {
		return []string{`{"action":"nodes","responseid":"nerve","nodes":{}}`}
	}})
	defer server.Close()

	// The system trust store does not know the test certificate.
	if _, err := New(Config{URL: server.URL, Username: "nerve-svc", Password: "s3cret", Timeout: 5 * time.Second}).Nodes(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an untrusted certificate was accepted: %v", err)
	}

	path := filepath.Join(t.TempDir(), "meshcentral-root.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots, err := LoadRootCAs(path)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := New(Config{URL: server.URL, Username: "nerve-svc", Password: "s3cret", Timeout: 5 * time.Second, RootCAs: roots}).Nodes(context.Background())
	if err != nil || len(nodes) != 0 {
		t.Fatalf("pinned root not used: %v", err)
	}
	if _, err := LoadRootCAs(filepath.Join(t.TempDir(), "missing.pem")); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("missing CA file accepted: %v", err)
	}
}
