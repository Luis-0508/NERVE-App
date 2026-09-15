# Local cartography

countries-50m.json: world-atlas 2.0.2, Natural Earth 1:50m administrative country boundaries.
https://github.com/topojson/world-atlas
https://www.naturalearthdata.com/about/terms-of-use/
world-atlas: ISC (see world-atlas-LICENSE); underlying Natural Earth data: public domain.
Bundled locally: no runtime tiles, CDN, geocoding or third-party requests.

internal/traffic/country-locations.json uses Natural Earth v5.1.2 label points,
ISO_A2_EH / ISO_N3_EH and NAME_EN. Each point is verified with d3.geoContains
against the exact countries-50m.json rendered above. Invalid label candidates use
an interior point of the largest polygon; a country without matching geometry is
left unlocated. These represent countries, never physical server locations.
Reproduce with `node scripts/generate-country-locations.mjs <downloaded GeoJSON>`:
https://raw.githubusercontent.com/nvkelso/natural-earth-vector/v5.1.2/geojson/ne_50m_admin_0_countries.geojson
Input SHA-256: 3e458fc036ad0a66411f2c1e6cac49c5d7bfb81cb1123bc513b22511a2b7fdeb.
`npm run test:map` verifies all generated representatives against the map geometry.
Remote city estimates: see DB-IP-NOTICE.md for attribution and accuracy limits.
Boundaries reflect the dataset's de facto representation, not a political endorsement.
