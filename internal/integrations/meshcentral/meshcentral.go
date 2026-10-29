// Package meshcentral is a read-only client for the MeshCentral control
// channel (/control.ashx). It lists nodes and the network facts NERVE needs to
// propose device bindings. It never opens sessions or runs commands.
//
// Protocol facts were checked against MeshCentral 1.3.1 (meshctrl.js and
// meshuser.js): password login uses the x-meshauth header, `nodes` replies
// carry the request's responseid, while `getnetworkinfo` and `lastconnect`
// replies are matched by action and node ID.
package meshcentral

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var (
	ErrNotConfigured        = errors.New("meshcentral integration is not configured")
	ErrInvalidConfiguration = errors.New("meshcentral invalid configuration")
	ErrAuthentication       = errors.New("meshcentral authentication failed")
	ErrUnavailable          = errors.New("meshcentral unreachable")
	ErrInvalidResponse      = errors.New("meshcentral invalid response")
)

const (
	// MaxNodes bounds one listing so a misbehaving server cannot exhaust memory.
	MaxNodes        = 5000
	maxMessageBytes = 8 << 20
	maxSkipped      = 1000
	responseID      = "nerve"
)

// Config holds the service account. The password never leaves this package:
// it is sent only in the WebSocket handshake header and never logged.
type Config struct {
	// URL is the MeshCentral base URL (https://, wss://, or http:// and ws://
	// for a Compose-internal hop). A ?key= login key is kept.
	URL      string
	Username string
	Password string
	Timeout  time.Duration
	// RootCAs, when set, replaces the system trust store, for example with
	// MeshCentral's self-signed root certificate.
	RootCAs *x509.CertPool
	// HTTPClient is used for the handshake; tests inject one.
	HTTPClient *http.Client
}

// Node is a MeshCentral device as the service account sees it.
type Node struct {
	ID       string `json:"id"`
	MeshID   string `json:"meshId"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	// Connectivity is MeshCentral's bitmask; bit 1 means the agent is connected.
	Connectivity int `json:"connectivity"`
	// MACs and Addresses come from getnetworkinfo and may be empty when the
	// account lacks device-details rights or the agent never reported them.
	MACs          []string   `json:"macs"`
	Addresses     []string   `json:"addresses"`
	LastConnectAt *time.Time `json:"lastConnectAt,omitempty"`
}

// AgentConnected reports whether the MeshAgent currently holds a connection.
func (n Node) AgentConnected() bool { return n.Connectivity&1 != 0 }

type Client struct {
	cfg Config
}

func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		// One budget for the whole listing: two round trips per node.
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.HTTPClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs}
		cfg.HTTPClient = &http.Client{Transport: transport}
	}
	return &Client{cfg: cfg}
}

// LoadRootCAs reads PEM certificates that replace the system trust store.
func LoadRootCAs(path string) (*x509.CertPool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read CA file: %v", ErrInvalidConfiguration, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(contents) {
		return nil, fmt.Errorf("%w: CA file holds no PEM certificate", ErrInvalidConfiguration)
	}
	return pool, nil
}

// Configured reports whether a URL and service account are set.
func (c *Client) Configured() bool {
	return c != nil && c.cfg.URL != "" && c.cfg.Username != "" && c.cfg.Password != ""
}

// ControlURL derives the WebSocket control endpoint from a base URL.
func ControlURL(base string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: URL must be an absolute URL without credentials", ErrInvalidConfiguration)
	}
	switch parsed.Scheme {
	case "https", "wss":
		parsed.Scheme = "wss"
	case "http", "ws":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("%w: unsupported URL scheme", ErrInvalidConfiguration)
	}
	query := url.Values{}
	if key := parsed.Query().Get("key"); key != "" {
		query.Set("key", key)
	}
	parsed.RawQuery = query.Encode()
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/control.ashx"
	return parsed.String(), nil
}

// Nodes lists every node the service account can see, with network facts and
// the last agent connection. One WebSocket connection serves the whole listing.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	controlURL, err := ControlURL(c.cfg.URL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	header := http.Header{}
	header.Set("x-meshauth", base64.StdEncoding.EncodeToString([]byte(c.cfg.Username))+","+base64.StdEncoding.EncodeToString([]byte(c.cfg.Password)))
	conn, response, err := websocket.Dial(ctx, controlURL, &websocket.DialOptions{HTTPHeader: header, HTTPClient: c.cfg.HTTPClient})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return nil, ErrAuthentication
		}
		// The dial error may include the URL with its login key; keep it out.
		return nil, ErrUnavailable
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(maxMessageBytes)
	session := &session{conn: conn}

	nodes, err := session.nodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if err := session.networkInfo(ctx, &nodes[i]); err != nil {
			return nil, err
		}
		if err := session.lastConnect(ctx, &nodes[i]); err != nil {
			return nil, err
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	return nodes, nil
}

type session struct {
	conn *websocket.Conn
}

type message struct {
	Action     string `json:"action"`
	ResponseID string `json:"responseid"`
	NodeID     string `json:"nodeid"`
	Cause      string `json:"cause"`
	Msg        string `json:"msg"`
	Result     any    `json:"result"`
	NoInfo     bool   `json:"noinfo"`
}

func (s *session) send(ctx context.Context, request map[string]any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := s.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return ErrUnavailable
	}
	return nil
}

// await reads messages until match accepts one. MeshCentral pushes unrelated
// messages (serverinfo, userinfo, events) at any time; they are skipped, but
// a "close" message ends the session with the server's reason.
func (s *session) await(ctx context.Context, match func(message) bool) ([]byte, message, error) {
	for range maxSkipped {
		kind, payload, err := s.conn.Read(ctx)
		if err != nil {
			if status := websocket.CloseStatus(err); status == websocket.StatusPolicyViolation || status == websocket.StatusMessageTooBig {
				return nil, message{}, fmt.Errorf("%w: %v", ErrInvalidResponse, status)
			}
			return nil, message{}, ErrUnavailable
		}
		if kind != websocket.MessageText {
			continue
		}
		var item message
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, message{}, fmt.Errorf("%w: malformed message", ErrInvalidResponse)
		}
		if item.Action == "close" {
			switch item.Cause {
			case "noauth", "locked", "banned":
				return nil, message{}, fmt.Errorf("%w: %s", ErrAuthentication, closeReason(item))
			}
			return nil, message{}, fmt.Errorf("%w: server closed the session", ErrUnavailable)
		}
		if match(item) {
			return payload, item, nil
		}
	}
	return nil, message{}, fmt.Errorf("%w: no reply", ErrInvalidResponse)
}

func closeReason(item message) string {
	switch {
	case item.Cause == "noauth" && item.Msg == "tokenrequired":
		return "the service account requires a second factor"
	case item.Cause == "noauth" && item.Msg == "nokey":
		return "the URL login key is missing or invalid"
	case item.Cause == "locked":
		return "the service account is locked"
	case item.Cause == "banned":
		return "too many failed logins"
	}
	return "invalid login"
}

type rawNode struct {
	ID     string `json:"_id"`
	Name   string `json:"name"`
	RName  string `json:"rname"`
	Host   string `json:"host"`
	OSDesc string `json:"osdesc"`
	Conn   int    `json:"conn"`
}

func (s *session) nodes(ctx context.Context) ([]Node, error) {
	if err := s.send(ctx, map[string]any{"action": "nodes", "responseid": responseID}); err != nil {
		return nil, err
	}
	payload, item, err := s.await(ctx, func(item message) bool { return item.Action == "nodes" && item.ResponseID == responseID })
	if err != nil {
		return nil, err
	}
	if text, ok := item.Result.(string); ok && text != "" {
		return nil, fmt.Errorf("%w: node listing refused", ErrInvalidResponse)
	}
	var reply struct {
		Nodes map[string][]rawNode `json:"nodes"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return nil, fmt.Errorf("%w: malformed node listing", ErrInvalidResponse)
	}
	nodes := make([]Node, 0)
	for meshID, items := range reply.Nodes {
		for _, raw := range items {
			if !ValidNodeID(raw.ID) {
				return nil, fmt.Errorf("%w: invalid node ID", ErrInvalidResponse)
			}
			if len(nodes) == MaxNodes {
				return nil, fmt.Errorf("%w: more than %d nodes", ErrInvalidResponse, MaxNodes)
			}
			// rname is the computer name the agent reports. The display name is
			// operator-editable and only counts when it could be a hostname.
			hostname := strings.TrimSpace(raw.RName)
			if name := strings.TrimSpace(raw.Name); hostname == "" && name != "" && !strings.ContainsAny(name, " \t") {
				hostname = name
			}
			nodes = append(nodes, Node{
				ID: raw.ID, MeshID: limit(meshID, 256), Name: limit(raw.Name, 255), Hostname: limit(strings.ToLower(strings.TrimSpace(hostname)), 255),
				OS: limit(raw.OSDesc, 255), Connectivity: raw.Conn, MACs: []string{}, Addresses: []string{},
			})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes, nil
}

type netInterface struct {
	MAC     string `json:"mac"`
	Address string `json:"address"`
}

func (s *session) networkInfo(ctx context.Context, node *Node) error {
	if err := s.send(ctx, map[string]any{"action": "getnetworkinfo", "nodeid": node.ID, "responseid": responseID}); err != nil {
		return err
	}
	payload, item, err := s.await(ctx, func(item message) bool { return item.Action == "getnetworkinfo" && item.NodeID == node.ID })
	if err != nil {
		return err
	}
	if item.NoInfo {
		return nil
	}
	var reply struct {
		Interfaces map[string][]netInterface `json:"netif2"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return fmt.Errorf("%w: malformed network information", ErrInvalidResponse)
	}
	macs, addresses := map[string]bool{}, map[string]bool{}
	for _, items := range reply.Interfaces {
		for _, item := range items {
			if mac := NormalizeMAC(item.MAC); mac != "" {
				macs[mac] = true
			}
			if address := strings.TrimSpace(item.Address); address != "" && len(address) <= 64 {
				addresses[address] = true
			}
		}
	}
	node.MACs, node.Addresses = sortedKeys(macs, 64), sortedKeys(addresses, 64)
	return nil
}

func (s *session) lastConnect(ctx context.Context, node *Node) error {
	if err := s.send(ctx, map[string]any{"action": "lastconnect", "nodeid": node.ID, "responseid": responseID}); err != nil {
		return err
	}
	payload, item, err := s.await(ctx, func(item message) bool { return item.Action == "lastconnect" && item.NodeID == node.ID })
	if err != nil {
		return err
	}
	if item.NoInfo {
		return nil
	}
	var reply struct {
		Time int64 `json:"time"`
	}
	if err := json.Unmarshal(payload, &reply); err != nil {
		return fmt.Errorf("%w: malformed last connection", ErrInvalidResponse)
	}
	if reply.Time > 0 {
		at := time.UnixMilli(reply.Time).UTC()
		node.LastConnectAt = &at
	}
	return nil
}

// ValidNodeID accepts MeshCentral node IDs of the form node/<domain>/<id>.
// The default domain is empty, giving node//<id>.
func ValidNodeID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] != "node" || parts[2] == "" || len(id) > 256 {
		return false
	}
	for _, r := range parts[1] + parts[2] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '@' || r == '$' || r == '.') {
			return false
		}
	}
	return true
}

// NormalizeMAC returns a lowercase colon-separated MAC address, or "" for
// malformed and all-zero values.
func NormalizeMAC(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ":"))
	parts := strings.Split(value, ":")
	if len(parts) != 6 || value == "00:00:00:00:00:00" {
		return ""
	}
	for _, part := range parts {
		if len(part) != 2 || strings.Trim(part, "0123456789abcdef") != "" {
			return ""
		}
	}
	return value
}

func sortedKeys(set map[string]bool, max int) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > max {
		keys = keys[:max]
	}
	return keys
}

func limit(value string, max int) string {
	if len(value) > max {
		value = value[:max]
	}
	// PostgreSQL text rejects invalid UTF-8, including a rune cut in half.
	return strings.ToValidUTF8(value, "")
}
