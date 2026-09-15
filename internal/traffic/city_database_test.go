package traffic

import (
	"context"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestLocationPrecision(t *testing.T) {
	lat, lon := 50.1109, 8.6821
	for _, city := range []string{"Frankfurt", ""} {
		f := Flow{}
		ApplyLocation(&f, GeoLocation{Country: "DE", City: city, Region: "Hesse", Latitude: &lat, Longitude: &lon})
		if f.RemoteLatitude == nil || *f.RemoteLatitude != lat || *f.RemoteLongitude != lon || f.RemoteLocationSource != "dbip-city-lite" {
			t.Fatal(f)
		}
	}
	// Country-only MMDB centroids must be discarded, even if numerically valid.
	oceanLat, oceanLon := 13.990, -39.849
	f := Flow{}
	ApplyLocation(&f, GeoLocation{Country: "DE", Latitude: &oceanLat, Longitude: &oceanLon})
	if f.RemoteLocationSource != "country-representative" || *f.RemoteLatitude != countryLocations["DE"].Lat {
		t.Fatal(f)
	}
	invalid := math.NaN()
	ApplyLocation(&f, GeoLocation{Country: "DE", City: "Invalid", Latitude: &invalid, Longitude: &lon})
	if f.RemoteLocationSource != "country-representative" {
		t.Fatal(f)
	}
	unknown := Flow{}
	ApplyLocation(&unknown, GeoLocation{})
	if unknown.RemoteLatitude != nil || unknown.RemoteLongitude != nil || unknown.RemoteLocationSource != "" {
		t.Fatal("invented location")
	}
	// Actual supplied coastal/island estimates are never snapped to mainland.
	ApplyLocation(&unknown, GeoLocation{Country: "NZ", City: "Island estimate", Latitude: &oceanLat, Longitude: &oceanLon})
	if *unknown.RemoteLongitude != oceanLon {
		t.Fatal("city estimate arbitrarily moved")
	}
}

type geoTestSource struct{ snapshot Snapshot }

func (s geoTestSource) Live(context.Context) (Snapshot, error) { return s.snapshot, nil }
func TestLocationEnrichmentPreservesSource(t *testing.T) {
	empty, _ := WithLocations(geoTestSource{}, nil).Live(context.Background())
	if empty.Flows == nil {
		t.Fatal("empty live flows must serialize as an array")
	}
	original := Snapshot{Flows: []Flow{{ID: "stable", RemoteIP: "1.1.1.1", FlowCount: 3, Bytes: 123}}, Summary: Summary{UnlocatedFlows: 3}}
	lat, lon := 52.37, 4.90
	source := WithLocations(geoTestSource{original}, func(netip.Addr) GeoLocation {
		return GeoLocation{Country: "NL", City: "Amsterdam", Latitude: &lat, Longitude: &lon}
	})
	got, err := source.Live(context.Background())
	if err != nil || got.Summary.UnlocatedFlows != 0 || got.Flows[0].ID != "stable" || got.Flows[0].Bytes != 123 || original.Flows[0].RemoteLatitude != nil {
		t.Fatal(got, err)
	}
}
func TestCityDatabaseInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.mmdb")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCities(path); err == nil {
		t.Fatal("accepted invalid database")
	}
}
func TestCityDatabaseReal(t *testing.T) {
	path := os.Getenv("NERVE_TEST_CITY_DATABASE")
	if path == "" {
		t.Skip("set NERVE_TEST_CITY_DATABASE to the pinned local DB-IP MMDB")
	}
	lookup, err := LoadCities(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"8.8.8.8", "2001:4860:4860::8888"} {
		location := lookup(netip.MustParseAddr(ip))
		if location.Country == "" || location.Latitude == nil || location.Longitude == nil || (location.City == "" && location.Region == "") {
			t.Fatalf("real database lookup missing for public test resolver %s", ip)
		}
	}
	if location := lookup(netip.MustParseAddr("192.168.1.1")); location.Latitude != nil {
		t.Fatal("private address geolocated")
	}
}
