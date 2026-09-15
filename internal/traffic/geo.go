package traffic

import (
	_ "embed"
	"encoding/json"
)

// Natural Earth 1:50m admin-0 countries, public domain. See public/maps/NOTICE.md.
//
//go:embed country-locations.json
var countryData []byte
var countryLocations = func() map[string]struct {
	Name     string
	Lat, Lon float64
} {
	var countries map[string]struct {
		Name     string
		Lat, Lon float64
	}
	if err := json.Unmarshal(countryData, &countries); err != nil {
		panic(err)
	}
	return countries
}()

func SetCountryLocation(flow *Flow) {
	country, ok := countryLocations[flow.RemoteCountryCode]
	if !ok {
		return
	}
	if flow.RemoteCountryName == "" {
		flow.RemoteCountryName = country.Name
	}
	lat, lon := country.Lat, country.Lon
	flow.RemoteLatitude, flow.RemoteLongitude = &lat, &lon
	flow.RemoteLocationSource = "country-representative"
}
