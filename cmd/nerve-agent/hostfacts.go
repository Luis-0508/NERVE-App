package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// collectHostFacts gathers the self-reported evidence Core uses to propose a
// device association. The raw machine ID never leaves the host; only a
// domain-separated SHA-256 digest is sent.
func collectHostFacts() agentproto.HostFacts {
	hostname, _ := os.Hostname()
	facts := agentproto.HostFacts{
		Hostname: hostname, OS: runtime.GOOS, OSVersion: truncateFact(osVersion()), Architecture: runtime.GOARCH,
		AgentVersion: version, Interfaces: []agentproto.InterfaceFact{},
	}
	if id := strings.TrimSpace(machineID()); id != "" {
		sum := sha256.Sum256([]byte("nerve-machine-id-v1:" + id))
		facts.MachineFingerprint = hex.EncodeToString(sum[:])
	}
	interfaces, _ := net.Interfaces()
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback != 0 || len(facts.Interfaces) >= agentproto.MaxInterfaces {
			continue
		}
		fact := agentproto.InterfaceFact{Name: truncateFact(item.Name), MAC: item.HardwareAddr.String(), Addresses: []string{}}
		addresses, _ := item.Addrs()
		for _, raw := range addresses {
			if len(fact.Addresses) >= agentproto.MaxAddressesPerIface {
				break
			}
			if address, _, err := net.ParseCIDR(raw.String()); err == nil && !address.IsLoopback() && !address.IsLinkLocalUnicast() {
				fact.Addresses = append(fact.Addresses, address.String())
			}
		}
		if fact.MAC == "" && len(fact.Addresses) == 0 {
			continue
		}
		facts.Interfaces = append(facts.Interfaces, fact)
	}
	return facts
}

func truncateFact(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > agentproto.MaxFactLength {
		return value[:agentproto.MaxFactLength]
	}
	return value
}
