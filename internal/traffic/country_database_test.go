package traffic

import (
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestCountryRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "countries.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	_, _ = gz.Write([]byte("1.1.1.0,1.1.1.255,AU\n2001:db8::,2001:db8::ffff,DE\n"))
	_ = gz.Close()
	_ = f.Close()
	lookup, err := LoadCountries(path)
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{"1.1.1.0": "AU", "1.1.1.255": "AU", "1.1.2.0": "", "::ffff:1.1.1.1": "AU", "2001:db8::42": "DE", "2001:db9::": ""} {
		if got := lookup(netip.MustParseAddr(raw)); got != want {
			t.Errorf("%s: %q != %q", raw, got, want)
		}
	}
}
