package traffic

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"os"

	"github.com/oschwald/maxminddb-golang/v2"
)

type GeoLocation struct {
	Country             string
	City                string
	Region              string
	Latitude, Longitude *float64
}
type LocationLookup func(netip.Addr) GeoLocation

// LoadCities loads a local DB-IP City Lite MMDB once. No IP lookup uses the network.
// OpenBytes avoids open file/mmap lifetime management; the closure owns the reader.
func LoadCities(path string) (LocationLookup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	db, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, err
	}
	if db.Metadata.DatabaseType != "DBIP-City-Lite" {
		return nil, fmt.Errorf("expected DBIP-City-Lite database")
	}
	return func(ip netip.Addr) GeoLocation {
		var row struct {
			Country struct {
				Code string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
			City struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"city"`
			Subdivisions []struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"subdivisions"`
			Location struct {
				Latitude  *float64 `maxminddb:"latitude"`
				Longitude *float64 `maxminddb:"longitude"`
			} `maxminddb:"location"`
		}
		if !ip.IsValid() || db.Lookup(ip.Unmap()).Decode(&row) != nil {
			return GeoLocation{}
		}
		location := GeoLocation{Country: row.Country.Code, City: row.City.Names["en"], Latitude: row.Location.Latitude, Longitude: row.Location.Longitude}
		if len(row.Subdivisions) > 0 {
			location.Region = row.Subdivisions[0].Names["en"]
		}
		return location
	}, nil
}

func ApplyLocation(flow *Flow, location GeoLocation) {
	if location.Country != "" {
		flow.RemoteCountryCode = location.Country
		flow.RemoteCountryName = ""
	}
	// Only location-specific coordinates qualify. Country-only MMDB coordinates can
	// themselves be ocean centroids; use our validated land representative instead.
	flow.RemoteLatitude, flow.RemoteLongitude = nil, nil
	flow.RemoteCity, flow.RemoteRegion, flow.RemoteLocationSource = "", "", ""
	SetCountryLocation(flow)
	lat, lon := location.Latitude, location.Longitude
	if (location.City == "" && location.Region == "") || lat == nil || lon == nil || math.IsNaN(*lat) || math.IsNaN(*lon) || math.IsInf(*lat, 0) || math.IsInf(*lon, 0) || math.Abs(*lat) > 90 || math.Abs(*lon) > 180 {
		return
	}
	// Keep actual city/region estimates unchanged: coastline simplification and tiny
	// islands must not cause arbitrary snapping to a different place.
	flow.RemoteLatitude, flow.RemoteLongitude = lat, lon
	flow.RemoteCity, flow.RemoteRegion = location.City, location.Region
	flow.RemoteLocationSource = "dbip-city-lite"
}

type locatedSource struct {
	source Source
	lookup LocationLookup
}

func WithLocations(source Source, lookup LocationLookup) Source { return locatedSource{source, lookup} }
func (s locatedSource) Live(ctx context.Context) (Snapshot, error) {
	snapshot, err := s.source.Live(ctx)
	if err != nil {
		return snapshot, err
	}
	// Sensor snapshots are shared under a lock; never mutate their backing slice.
	flows := make([]Flow, len(snapshot.Flows))
	copy(flows, snapshot.Flows)
	snapshot.Flows = flows
	// Preserve counts for source flows truncated out of the displayed groups.
	for i := range snapshot.Flows {
		flow := &snapshot.Flows[i]
		unlocatedBefore := flow.RemoteLatitude == nil || flow.RemoteLongitude == nil
		location := GeoLocation{}
		if ip, e := netip.ParseAddr(flow.RemoteIP); e == nil && s.lookup != nil {
			location = s.lookup(ip)
		}
		ApplyLocation(flow, location)
		unlocatedAfter := flow.RemoteLatitude == nil || flow.RemoteLongitude == nil
		if unlocatedBefore != unlocatedAfter {
			count := flow.FlowCount
			if count < 1 {
				count = 1
			}
			if unlocatedAfter {
				snapshot.Summary.UnlocatedFlows += count
			} else {
				snapshot.Summary.UnlocatedFlows -= count
			}
		}
	}
	return snapshot, nil
}
