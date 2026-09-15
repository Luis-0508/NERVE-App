package traffic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

type Flow struct {
	ID                   string    `json:"id"`
	LocalIP              string    `json:"localIp"`
	RemoteIP             string    `json:"remoteIp"`
	LocalPort            uint16    `json:"localPort"`
	RemotePort           uint16    `json:"remotePort"`
	LocalPorts           []uint16  `json:"localPorts"`
	RemotePorts          []uint16  `json:"remotePorts"`
	FlowCount            int       `json:"flowCount"`
	Direction            Direction `json:"direction"`
	Protocol             string    `json:"protocol"`
	Application          string    `json:"application"`
	Bytes                uint64    `json:"bytes"`
	BytesPerSecond       float64   `json:"bytesPerSecond"`
	FirstSeenAt          time.Time `json:"firstSeenAt"`
	LastSeenAt           time.Time `json:"lastSeenAt"`
	RemoteCountryCode    string    `json:"remoteCountryCode,omitempty"`
	RemoteCountryName    string    `json:"remoteCountryName,omitempty"`
	RemoteLatitude       *float64  `json:"remoteLatitude,omitempty"`
	RemoteLongitude      *float64  `json:"remoteLongitude,omitempty"`
	RemoteCity           string    `json:"remoteCity,omitempty"`
	RemoteRegion         string    `json:"remoteRegion,omitempty"`
	RemoteLocationSource string    `json:"remoteLocationSource,omitempty"`
}
type Summary struct {
	ActiveFlows            int     `json:"activeFlows"`
	AggregatedFlows        int     `json:"aggregatedFlows"`
	UnlocatedFlows         int     `json:"unlocatedFlows"`
	BytesPerSecond         float64 `json:"bytesPerSecond"`
	InboundBytesPerSecond  float64 `json:"inboundBytesPerSecond"`
	OutboundBytesPerSecond float64 `json:"outboundBytesPerSecond"`
}
type Snapshot struct {
	Capture          *Capture  `json:"capture,omitempty"`
	GeneratedAt      time.Time `json:"generatedAt"`
	Source           string    `json:"source"`
	Flows            []Flow    `json:"flows"`
	Summary          Summary   `json:"summary"`
	Truncated        bool      `json:"truncated"`
	SampledFlows     int       `json:"sampledFlows"`
	SourceTotalFlows int       `json:"sourceTotalFlows"`
}
type Source interface {
	Live(context.Context) (Snapshot, error)
}

func IsLocalIP(raw string, configured []netip.Prefix) bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
		return true
	}
	for _, prefix := range configured {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
func Classify(client, server string, configured []netip.Prefix) (localIP, remoteIP string, direction Direction, ok bool) {
	a, err := netip.ParseAddr(client)
	if err != nil {
		return
	}
	b, err := netip.ParseAddr(server)
	if err != nil {
		return
	}
	a, b = a.Unmap(), b.Unmap()
	if !a.IsGlobalUnicast() || !b.IsGlobalUnicast() {
		return
	}
	cl, sl := IsLocalIP(a.String(), configured), IsLocalIP(b.String(), configured)
	if cl == sl {
		return
	}
	if cl {
		return a.String(), b.String(), DirectionOutbound, true
	}
	return b.String(), a.String(), DirectionInbound, true
}
func StableID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:12])
}
func addPort(ports []uint16, port uint16) []uint16 {
	for _, v := range ports {
		if v == port {
			return ports
		}
	}
	return append(ports, port)
}
func Aggregate(flows []Flow, limit int) ([]Flow, Summary) {
	groups := map[string]*Flow{}
	// Stable destination groups across all poll sizes; all contributing ports remain explicit.
	for _, flow := range flows {
		key := strings.Join([]string{flow.RemoteIP, flow.LocalIP, flow.Protocol, flow.Application, string(flow.Direction)}, "\x00")
		current := groups[key]
		if current == nil {
			copy := flow
			copy.ID = StableID(key)
			copy.FlowCount = 1
			copy.LocalPorts = []uint16{flow.LocalPort}
			copy.RemotePorts = []uint16{flow.RemotePort}
			groups[key] = &copy
			continue
		}
		current.Bytes += flow.Bytes
		current.BytesPerSecond += flow.BytesPerSecond
		current.FlowCount++
		current.LocalPorts = addPort(current.LocalPorts, flow.LocalPort)
		current.RemotePorts = addPort(current.RemotePorts, flow.RemotePort)
		if flow.FirstSeenAt.Before(current.FirstSeenAt) {
			current.FirstSeenAt = flow.FirstSeenAt
		}
		if flow.LastSeenAt.After(current.LastSeenAt) {
			current.LastSeenAt = flow.LastSeenAt
		}
	}
	items := make([]Flow, 0, len(groups))
	var summary Summary
	for _, flow := range groups {
		sort.Slice(flow.LocalPorts, func(i, j int) bool { return flow.LocalPorts[i] < flow.LocalPorts[j] })
		sort.Slice(flow.RemotePorts, func(i, j int) bool { return flow.RemotePorts[i] < flow.RemotePorts[j] })
		if len(flow.LocalPorts) > 1 {
			flow.LocalPort = 0
		}
		if len(flow.RemotePorts) > 1 {
			flow.RemotePort = 0
		}
		items = append(items, *flow)
		summary.ActiveFlows++
		summary.AggregatedFlows++
		if flow.RemoteLatitude == nil || flow.RemoteLongitude == nil {
			summary.UnlocatedFlows += flow.FlowCount
		}
		summary.BytesPerSecond += flow.BytesPerSecond
		if flow.Direction == DirectionInbound {
			summary.InboundBytesPerSecond += flow.BytesPerSecond
		} else {
			summary.OutboundBytesPerSecond += flow.BytesPerSecond
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].BytesPerSecond == items[j].BytesPerSecond {
			return items[i].ID < items[j].ID
		}
		return items[i].BytesPerSecond > items[j].BytesPerSecond
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, summary
}
