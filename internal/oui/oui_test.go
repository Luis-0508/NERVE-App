package oui

import "testing"

func TestLookupPrefersLongestAssignment(t *testing.T) {
	table := parse("# comment\nA8BBCC\tLarge block\r\nA8BBCC1\tMedium block\nA8BBCC123\tSmall block\n\nmalformed line\n")
	for _, test := range []struct {
		mac, vendor string
		ok          bool
	}{
		{"a8:bb:cc:1f:34:56", "Medium block", true},
		{"A8:BB:CC:12:3F:FF", "Small block", true},
		{"a8:bb:cc:f0:00:00", "Large block", true},
		{" a8-bb-cc-00-00-01 ", "Large block", true},
		{"a8bb.cc1f.3456", "Medium block", true},
		{"ac:bb:cc:12:34:56", "", false},                                           // unknown block
		{"aa:bb:cc:12:34:56", "", false},                                           // locally administered
		{"a9:bb:cc:12:34:56", "", false},                                           // multicast
		{"ab:bb:cc:12:34:56", "", false},                                           // both bits
		{"", "", false},                                                            // empty
		{"not-a-mac", "", false},                                                   // malformed
		{"a8:bb:cc:12:34", "", false},                                              // too short
		{"a8:bb:cc:12:34:56:78:9a", "", false},                                     // EUI-64
		{"a8:bb:cc:12:34:56:78:9a:bc:de:f0:12:34:56:78:9a:bc:de:f0:12", "", false}, // InfiniBand
	} {
		if vendor, ok := lookupIn(table, test.mac); vendor != test.vendor || ok != test.ok {
			t.Errorf("lookupIn(%q) = %q, %v; want %q, %v", test.mac, vendor, ok, test.vendor, test.ok)
		}
	}
}

func TestLookupUsesEmbeddedRegistry(t *testing.T) {
	for _, test := range []struct{ mac, vendor string }{
		{"00:15:5d:01:02:03", "Microsoft Corporation"},   // MA-L
		{"00:55:da:01:02:03", "Shinko Technos co.,ltd."}, // MA-M
		{"00:1b:c5:00:00:01", "Converging Systems Inc."}, // MA-S
	} {
		if vendor, ok := Lookup(test.mac); !ok || vendor != test.vendor {
			t.Errorf("Lookup(%q) = %q, %v; want %q", test.mac, vendor, ok, test.vendor)
		}
	}
	// 00:1B:C5 is an MA-L block the IEEE subdivided into MA-S assignments; an
	// address outside all of them must not be attributed to the registry itself.
	if vendor, ok := Lookup("00:1b:c5:ff:ff:ff"); ok {
		t.Errorf("unassigned sub-block resolved to %q", vendor)
	}
	if _, ok := Lookup("da:a1:19:00:00:01"); ok {
		t.Error("randomized (locally administered) address resolved to a vendor")
	}
}
