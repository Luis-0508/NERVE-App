// Package oui resolves the organization a MAC address block is assigned to,
// from an embedded copy of the IEEE Registration Authority listings. Lookups
// never touch the network; scripts/generate-oui.mjs regenerates the table.
package oui

import (
	_ "embed"
	"encoding/hex"
	"net"
	"strings"
	"sync"
)

//go:embed registry.tsv
var registry string

// Prefix lengths in hex digits: MA-S (36 bit), MA-M (28 bit), MA-L (24 bit),
// longest first so a sub-assignment wins over the block it was carved from.
var prefixDigits = [...]int{9, 7, 6}

var (
	loadOnce sync.Once
	vendors  map[string]string
)

func parse(text string) map[string]string {
	table := make(map[string]string, 60000)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		if prefix, vendor, ok := strings.Cut(line, "\t"); ok {
			table[prefix] = vendor
		}
	}
	return table
}

// Lookup returns the organization assigned the block containing mac. It
// reports false for malformed input, for addresses that are not 48-bit, and
// for multicast and locally administered addresses (including randomized
// MACs), whose leading bits say nothing about the manufacturer.
func Lookup(mac string) (string, bool) {
	loadOnce.Do(func() { vendors = parse(registry) })
	return lookupIn(vendors, mac)
}

func lookupIn(table map[string]string, mac string) (string, bool) {
	hardware, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil || len(hardware) != 6 || hardware[0]&0x03 != 0 {
		return "", false
	}
	digits := strings.ToUpper(hex.EncodeToString(hardware[:5]))
	for _, n := range prefixDigits {
		if vendor, ok := table[digits[:n]]; ok {
			return vendor, true
		}
	}
	return "", false
}
