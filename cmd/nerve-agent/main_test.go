package main

import (
	"net/netip"
	"testing"
)

func TestEnumerateIPv4SubnetSkipsNetworkAndBroadcast(t *testing.T) {
	t.Parallel()
	addresses := enumerate(netip.MustParsePrefix("192.0.2.0/30"))
	if len(addresses) != 2 || addresses[0].String() != "192.0.2.1" || addresses[1].String() != "192.0.2.2" {
		t.Fatalf("unexpected addresses: %v", addresses)
	}
}

func TestEnumerateIPv6Subnet(t *testing.T) {
	t.Parallel()
	addresses := enumerate(netip.MustParsePrefix("fd00::/126"))
	if len(addresses) != 4 {
		t.Fatalf("got %d addresses, want 4", len(addresses))
	}
}
