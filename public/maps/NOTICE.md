# Local cartography

countries-50m.json: world-atlas 2.0.2, Natural Earth 1:50m administrative country boundaries.
https://github.com/topojson/world-atlas
https://www.naturalearthdata.com/about/terms-of-use/
world-atlas: ISC (see world-atlas-LICENSE); underlying Natural Earth data: public domain.
Bundled locally: no runtime tiles, CDN, geocoding or third-party requests.

internal/traffic/country-centroids.json is derived using d3.geoCentroid from Natural Earth
ne_50m_admin_0_countries.geojson (ISO_A2_EH and NAME_EN), downloaded 2026-09-11:
https://github.com/nvkelso/natural-earth-vector/blob/master/geojson/ne_50m_admin_0_countries.geojson
These are approximate country centroids, not exact IP coordinates or verified physical server locations.
Boundaries reflect the dataset's de facto representation, not a political endorsement.
