package discovery

import "testing"

func TestNormalizeCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      string
		addresses int
		wantError bool
	}{
		{name: "IPv4 /24", input: "192.168.178.0/24", want: "192.168.178.0/24", addresses: 254},
		{name: "IPv4 point to point", input: "10.0.0.0/31", want: "10.0.0.0/31", addresses: 2},
		{name: "IPv6 small subnet", input: "fd00::/120", want: "fd00::/120", addresses: 256},
		{name: "host bits set", input: "192.168.178.4/24", wantError: true},
		{name: "too broad", input: "10.0.0.0/8", wantError: true},
		{name: "invalid", input: "not-a-subnet", wantError: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, addresses, err := NormalizeCIDR(test.input)
			if test.wantError {
				if err == nil {
					t.Fatalf("NormalizeCIDR(%q) unexpectedly succeeded", test.input)
				}
				return
			}
			if err != nil || got != test.want || addresses != test.addresses {
				t.Fatalf("NormalizeCIDR(%q) = %q, %d, %v; want %q, %d", test.input, got, addresses, err, test.want, test.addresses)
			}
		})
	}
}

func TestNormalizeMAC(t *testing.T) {
	t.Parallel()
	if got := normalizeMAC("AA-BB-CC-DD-EE-FF"); got != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("normalizeMAC returned %q", got)
	}
	if got := normalizeMAC("invalid"); got != "" {
		t.Fatalf("invalid MAC returned %q", got)
	}
}
