package traffic

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
)

type countryRange struct {
	first, last netip.Addr
	code        string
}

// LoadCountries reads the pinned DB-IP Country Lite CSV once, never sends IPs to
// a geolocation web service. Missing locations remain explicitly unlocated.
func LoadCountries(path string) (func(netip.Addr) string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := csv.NewReader(gz)
	reader.FieldsPerRecord = 3
	var ranges []countryRange
	for {
		row, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		a, e1 := netip.ParseAddr(row[0])
		b, e2 := netip.ParseAddr(row[1])
		if e1 != nil || e2 != nil || a.Compare(b) > 0 || len(row[2]) != 2 {
			return nil, fmt.Errorf("invalid country range")
		}
		ranges = append(ranges, countryRange{a.Unmap(), b.Unmap(), row[2]})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].first.Compare(ranges[j].first) < 0 })
	return func(ip netip.Addr) string {
		ip = ip.Unmap()
		i := sort.Search(len(ranges), func(i int) bool { return ranges[i].first.Compare(ip) > 0 }) - 1
		if i >= 0 && ranges[i].last.Compare(ip) >= 0 {
			return ranges[i].code
		}
		return ""
	}, nil
}
