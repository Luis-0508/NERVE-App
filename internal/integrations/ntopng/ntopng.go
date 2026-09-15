package ntopng

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nerve-labs/nerve/internal/traffic"
)

var (
	ErrNotConfigured        = errors.New("ntopng traffic source is not configured")
	ErrAuthentication       = errors.New("ntopng authentication failed")
	ErrUnavailable          = errors.New("ntopng traffic source unreachable")
	ErrInvalidResponse      = errors.New("ntopng invalid response")
	ErrInvalidConfiguration = errors.New("ntopng invalid configuration")
)

type Config struct {
	URL, Username, Password, InterfaceID string
	Timeout, CacheTTL                    time.Duration
	LocalCIDRs                           []netip.Prefix
}
type Client struct {
	cfg       Config
	http      *http.Client
	mu        sync.Mutex
	cached    traffic.Snapshot
	cachedAt  time.Time
	cachedErr error
}

func New(cfg Config) *Client {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 1500 * time.Millisecond
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.InterfaceID == "" {
		cfg.InterfaceID = "0"
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Live(ctx context.Context) (traffic.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cachedAt.IsZero() && time.Since(c.cachedAt) < c.cfg.CacheTTL {
		return c.cached, c.cachedErr
	}
	// One budget for the entire snapshot, including all pages and optional enrichment.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	snapshot, err := c.fetch(ctx)
	c.cachedErr = err
	c.cached, c.cachedAt = snapshot, time.Now()
	if err != nil {
		return traffic.Snapshot{}, err
	}
	return snapshot, nil
}

type response struct {
	RC  *int `json:"rc"`
	RSP struct {
		Data      json.RawMessage `json:"data"`
		TotalRows int             `json:"totalRows"`
	} `json:"rsp"`
}
type endpoint struct {
	IP      string `json:"ip"`
	Port    uint16 `json:"port"`
	Country string `json:"country"`
}
type rawFlow struct {
	HashID    string          `json:"hash_id"`
	VLAN      int             `json:"vlan"`
	Client    endpoint        `json:"client"`
	Server    endpoint        `json:"server"`
	FirstSeen float64         `json:"first_seen"`
	LastSeen  float64         `json:"last_seen"`
	Bytes     json.RawMessage `json:"bytes"`
	Protocol  struct {
		L4 string `json:"l4"`
		L7 string `json:"l7"`
	} `json:"protocol"`
	Thpt struct {
		BPS *float64 `json:"bps"`
	} `json:"thpt"`
}

// Only the documented active.lua schema is accepted; active_list.lua is a different API.
func (c *Client) get(ctx context.Context, target *url.URL, payload any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ErrInvalidConfiguration
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrAuthentication
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		if strings.Contains(strings.ToLower(res.Header.Get("Location")), "login") {
			return ErrAuthentication
		}
		return fmt.Errorf("%w: unexpected redirect; configure the final base URL", ErrInvalidResponse)
	}
	if res.StatusCode >= 500 {
		return ErrUnavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d", ErrInvalidResponse, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return ErrUnavailable
	}
	if len(body) > 8<<20 || json.Unmarshal(body, payload) != nil {
		return ErrInvalidResponse
	}
	return nil
}
func decodeRows[T any](payload response) ([]T, error) {
	if payload.RC == nil || *payload.RC != 0 {
		return nil, ErrInvalidResponse
	}
	data := strings.TrimSpace(string(payload.RSP.Data))
	// dkjson can encode an empty Lua table as {}.
	if data == "{}" {
		return []T{}, nil
	}
	if !strings.HasPrefix(data, "[") {
		return nil, ErrInvalidResponse
	}
	var rows []T
	if json.Unmarshal(payload.RSP.Data, &rows) != nil {
		return nil, ErrInvalidResponse
	}
	return rows, nil
}
func (c *Client) fetch(ctx context.Context) (traffic.Snapshot, error) {
	if strings.TrimSpace(c.cfg.URL) == "" || strings.TrimSpace(c.cfg.Username) == "" || c.cfg.Password == "" {
		return traffic.Snapshot{}, ErrNotConfigured
	}
	base, err := url.Parse(strings.TrimSpace(c.cfg.URL))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return traffic.Snapshot{}, ErrInvalidConfiguration
	}
	interfaceID, interfaceName := c.cfg.InterfaceID, ""
	if interfaceID == "auto" {
		// A single interface is unambiguous. Never silently pick one of several sensors.
		target := *base
		target.Path = strings.TrimRight(base.Path, "/") + "/lua/rest/v2/get/ntopng/interfaces.lua"
		var interfaces struct {
			RC  *int `json:"rc"`
			RSP []struct {
				ID   *int   `json:"ifid"`
				Name string `json:"ifname"`
			} `json:"rsp"`
		}
		if err := c.get(ctx, &target, &interfaces); err != nil {
			return traffic.Snapshot{}, err
		}
		if interfaces.RC == nil || *interfaces.RC != 0 {
			return traffic.Snapshot{}, ErrInvalidResponse
		}
		if len(interfaces.RSP) != 1 {
			return traffic.Snapshot{}, ErrInvalidConfiguration
		}
		selected := interfaces.RSP[0]
		if selected.ID == nil || *selected.ID < 0 || selected.Name == "" {
			return traffic.Snapshot{}, ErrInvalidResponse
		}
		interfaceID, interfaceName = strconv.Itoa(*selected.ID), selected.Name
	}
	if id, err := strconv.Atoi(interfaceID); err != nil || id < 0 {
		return traffic.Snapshot{}, ErrInvalidConfiguration
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/lua/rest/v2/get/flow/active.lua"
	q := base.Query()
	q.Set("ifid", interfaceID)
	q.Set("perPage", "500")
	q.Set("sortColumn", "thpt")
	q.Set("sortOrder", "desc")
	var rows []rawFlow
	seen := map[string]bool{}
	total := 0
	for page := 1; page <= 10; page++ {
		q.Set("currentPage", strconv.Itoa(page))
		base.RawQuery = q.Encode()
		var payload response
		if err := c.get(ctx, base, &payload); err != nil {
			return traffic.Snapshot{}, err
		}
		batch, err := decodeRows[rawFlow](payload)
		if err != nil {
			return traffic.Snapshot{}, err
		}
		total = payload.RSP.TotalRows
		added := 0
		for _, raw := range batch {
			if _, err := netip.ParseAddr(raw.Client.IP); err != nil {
				return traffic.Snapshot{}, ErrInvalidResponse
			}
			if _, err := netip.ParseAddr(raw.Server.IP); err != nil {
				return traffic.Snapshot{}, ErrInvalidResponse
			}
			if raw.Thpt.BPS == nil || *raw.Thpt.BPS < 0 || raw.Protocol.L4 == "" {
				return traffic.Snapshot{}, ErrInvalidResponse
			}
			if _, err := parseBytes(raw.Bytes); err != nil {
				return traffic.Snapshot{}, err
			}
			id := traffic.StableID(raw.HashID, raw.Client.IP, strconv.Itoa(int(raw.Client.Port)), raw.Server.IP, strconv.Itoa(int(raw.Server.Port)), raw.Protocol.L4, strconv.Itoa(raw.VLAN))
			if !seen[id] {
				seen[id] = true
				rows = append(rows, raw)
				added++
			}
		}
		if added == 0 || total == 0 || len(rows) >= total {
			break
		}
	}
	countries := map[string]string{}
	for _, raw := range rows {
		if raw.Client.Country == "" || raw.Server.Country == "" {
			countries = c.remoteCountries(ctx, base)
			break
		}
	}
	flows := make([]traffic.Flow, 0, len(rows))
	for _, raw := range rows {
		local, remote, direction, ok := traffic.Classify(raw.Client.IP, raw.Server.IP, c.cfg.LocalCIDRs)
		if !ok {
			continue
		}
		localPort, remotePort, country := raw.Client.Port, raw.Server.Port, raw.Server.Country
		if direction == traffic.DirectionInbound {
			localPort, remotePort, country = raw.Server.Port, raw.Client.Port, raw.Client.Country
		}
		if country == "" {
			country = countries[remote]
		}
		bytes, _ := parseBytes(raw.Bytes)
		flow := traffic.Flow{ID: traffic.StableID(raw.HashID, raw.Client.IP, strconv.Itoa(int(raw.Client.Port)), raw.Server.IP, strconv.Itoa(int(raw.Server.Port)), raw.Protocol.L4, strconv.Itoa(raw.VLAN)), LocalIP: local, RemoteIP: remote, LocalPort: localPort, RemotePort: remotePort, Direction: direction, Protocol: raw.Protocol.L4, Application: raw.Protocol.L7, Bytes: bytes, BytesPerSecond: *raw.Thpt.BPS / 8, FirstSeenAt: epoch(raw.FirstSeen), LastSeenAt: epoch(raw.LastSeen), RemoteCountryCode: strings.ToUpper(country)}
		if flow.Application == "" {
			flow.Application = "Unknown"
		}
		traffic.SetCountryLocation(&flow)
		flows = append(flows, flow)
	}
	items, summary := traffic.Aggregate(flows, 500)
	summary.ActiveFlows = len(flows)
	return traffic.Snapshot{Capture: &traffic.Capture{InterfaceID: interfaceID, InterfaceName: interfaceName}, GeneratedAt: time.Now().UTC(), Source: "ntopng", Flows: items, Summary: summary, Truncated: len(rows) < total || len(items) < summary.AggregatedFlows, SampledFlows: len(rows), SourceTotalFlows: total}, nil
}
func (c *Client) remoteCountries(ctx context.Context, activeURL *url.URL) map[string]string {
	endpoint := *activeURL
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/flow/active.lua") + "/host/active.lua"
	q := endpoint.Query()
	q.Set("mode", "remote")
	q.Set("sortColumn", "traffic")
	countries := map[string]string{}
	count := 0
	for page := 1; page <= 10; page++ {
		q.Set("currentPage", strconv.Itoa(page))
		endpoint.RawQuery = q.Encode()
		var payload response
		if c.get(ctx, &endpoint, &payload) != nil {
			break
		}
		rows, err := decodeRows[struct {
			IP      string `json:"ip"`
			Country string `json:"country"`
		}](payload)
		if err != nil {
			break
		}
		previous := len(countries)
		for _, host := range rows {
			countries[host.IP] = host.Country
		}
		count += len(rows)
		if len(countries) == previous || payload.RSP.TotalRows == 0 || count >= payload.RSP.TotalRows {
			break
		}
	}
	return countries
}
func epoch(value float64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(value), 0).UTC()
}
func parseBytes(raw json.RawMessage) (uint64, error) {
	var n uint64
	if json.Unmarshal(raw, &n) == nil && string(raw) != "null" {
		return n, nil
	}
	var obj struct {
		Total *uint64 `json:"total"`
		Sent  uint64  `json:"sent"`
		Recvd uint64  `json:"recvd"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Total != nil {
		return *obj.Total, nil
	}
	return 0, ErrInvalidResponse
}
