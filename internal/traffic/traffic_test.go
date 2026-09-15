package traffic

import (
	"net/netip"
	"testing"
	"time"
)

func TestClassifyLocalAndExternalTraffic(t *testing.T) {
	if local, remote, direction, ok := Classify("192.168.178.50", "1.1.1.1", nil); !ok || local != "192.168.178.50" || remote != "1.1.1.1" || direction != DirectionOutbound {
		t.Fatalf("unexpected outbound classification: %q %q %q %t", local, remote, direction, ok)
	}
	if local, remote, direction, ok := Classify("142.250.1.1", "192.168.178.50", nil); !ok || local != "192.168.178.50" || remote != "142.250.1.1" || direction != DirectionInbound {
		t.Fatalf("unexpected inbound classification: %q %q %q %t", local, remote, direction, ok)
	}
	if _, _, _, ok := Classify("192.168.178.50", "192.168.178.60", nil); ok {
		t.Fatal("local-to-local flow must not be mapped")
	}
	if _, _, _, ok := Classify("8.8.8.8", "1.1.1.1", nil); ok {
		t.Fatal("external-to-external flow must not be mapped")
	}
	prefix := netip.MustParsePrefix("100.64.0.0/10")
	if !IsLocalIP("100.64.1.4", []netip.Prefix{prefix}) {
		t.Fatal("configured discovery subnet must be local")
	}
}

func TestAggregateCombinesSessionsAndSummarizes(t *testing.T) {
	now := time.Now()
	flows, summary := Aggregate([]Flow{{RemoteIP: "1.1.1.1", LocalIP: "192.168.1.2", Application: "DNS", Protocol: "UDP", Direction: DirectionOutbound, Bytes: 10, BytesPerSecond: 2, FirstSeenAt: now}, {RemoteIP: "1.1.1.1", LocalIP: "192.168.1.2", Application: "DNS", Protocol: "UDP", Direction: DirectionOutbound, Bytes: 20, BytesPerSecond: 3, FirstSeenAt: now}, {RemoteIP: "8.8.8.8", LocalIP: "192.168.1.2", Application: "TLS", Protocol: "TCP", Direction: DirectionInbound, BytesPerSecond: 4, FirstSeenAt: now}}, 150)
	if len(flows) != 2 || flows[0].BytesPerSecond != 5 || flows[0].Bytes != 30 {
		t.Fatalf("sessions were not aggregated: %#v", flows)
	}
	if summary.ActiveFlows != 2 || summary.OutboundBytesPerSecond != 5 || summary.InboundBytesPerSecond != 4 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestInvalidAndNonUnicastAreNeverExternal(t *testing.T) {
	for _, remote := range []string{"not-an-ip", "0.0.0.0", "224.0.0.1", "255.255.255.255", "ff02::1", "::"} {
		if _, _, _, ok := Classify("10.0.0.1", remote, nil); ok {
			t.Fatalf("accepted %s", remote)
		}
	}
	if _, _, _, ok := Classify("::ffff:10.0.0.1", "::ffff:8.8.8.8", nil); !ok {
		t.Fatal("IPv4-mapped IPv6 not classified")
	}
}
func TestStableAggregationAcrossPollsAndLimit(t *testing.T) {
	input := []Flow{{ID: "one", LocalIP: "10.0.0.1", RemoteIP: "8.8.8.8", LocalPort: 50000, RemotePort: 53, BytesPerSecond: 10}, {ID: "two", LocalIP: "10.0.0.1", RemoteIP: "8.8.8.8", LocalPort: 50001, RemotePort: 53, BytesPerSecond: 20}}
	first, _ := Aggregate(input, 500)
	next, _ := Aggregate(input[1:], 500)
	if first[0].ID != next[0].ID || first[0].LocalPort != 0 || len(first[0].LocalPorts) != 2 || first[0].RemotePort != 53 {
		t.Fatal("unstable identity or misleading ports")
	}
	input = append(input, Flow{RemoteIP: "1.1.1.1", BytesPerSecond: 7})
	limited, summary := Aggregate(input, 1)
	if len(limited) != 1 || summary.BytesPerSecond != 37 || summary.AggregatedFlows != 2 {
		t.Fatal("limit lost summary data")
	}
}
func TestCountryCoverage(t *testing.T) {
	for _, code := range []string{"DE", "US", "ZA", "NZ", "AR", "ID", "KE"} {
		f := Flow{RemoteCountryCode: code}
		SetCountryLocation(&f)
		if f.RemoteLatitude == nil || f.RemoteLongitude == nil {
			t.Fatalf("missing country %s", code)
		}
	}
	f := Flow{RemoteCountryCode: "ZZ"}
	SetCountryLocation(&f)
	if f.RemoteLatitude != nil {
		t.Fatal("unknown country placed arbitrarily")
	}
}
