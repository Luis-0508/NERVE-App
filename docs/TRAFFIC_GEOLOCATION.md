# Traffic map geography

## Local site and the Atlantic value

The old implementation stored only `{lat, lon}` under `nerve:traffic-map-origin`
and restored any numerically valid coordinates on every reload. There was no source,
timestamp, accuracy or migration version. Repository/history inspection found no
hardcoded `13.990, -39.849` default and no map-click location setter. In the shipped
component, the writers were manual Save and the explicit Browser action. However,
localStorage is shared by all pages on that browser origin, and a prior value contains
no audit information. The exact event that wrote the reported value cannot be
reconstructed honestly from those coordinates alone. The confirmed defect was accepting
an unverifiable stored value indefinitely, not an identified projection offset.

The new key `nerve:traffic-map-local-site:v2` stores version, coordinates, source
(`manual` or `browser`), ISO timestamp and browser-reported accuracy in metres.
Explicit manual locations do not expire. On reload, browser estimates older than 30 days
require a fresh action or manual confirmation. No location is requested automatically,
even if browser permission was already granted. Browser requests reject cached fixes.
There is no automatic public-IP lookup. Browser location may itself use the browser/OS
provider according to its permission policy; NERVE does not send it traffic history.

Legacy values and stale/invalid records are not used as the origin. Valid candidates
remain visible in Local site settings with an explanation. A previously user-entered
legacy location is preserved for review, but must be saved once to establish provenance;
the old format cannot distinguish it from other legacy values. Invalid values are not
plotted. Clear explicitly records an unset state so old values cannot revive on reload.
Unavailable storage permits manual selection for the current visit only. Nothing
changes `.env`, database, secrets or discovery settings.

Without a confirmed site the map says `Local site not set` and draws no origin/arcs.
The large marker is the user-selected site. Browser estimates are labelled and their
reported accuracy/time appear in Diagnostics; manual accuracy is unspecified.

## Remote locations

The API image bundles DB-IP City Lite September 2026, IPv4 and IPv6, MMDB format.
The build downloads a checksum-pinned archive and decompresses it; runtime queries
stay local. The MMDB reader loads the roughly 121 MB database once and searches its
indexed IP ranges, without loading millions of CSV objects. Both Windows-native and
external ntopng snapshots receive the same enrichment. Sensor-reported coordinates
remain untrusted; HMAC/replay validation and capture processing are unchanged.

1. City/region record with finite, valid coordinates: use its unmodified estimate.
2. Country only (including legacy Country Lite or ntopng codes): use a representative
   verified inside that country's exact locally rendered Natural Earth geometry.
3. No matching usable geography: retain the flow and counters/list, omit coordinates.

The 235 country representatives come from Natural Earth label points, with an interior
point of the largest polygon if necessary. Generation uses spherical `geoContains`;
`npm run test:map` independently checks every output against bundled world-atlas.
No country without matching geometry borrows a neighbour's point. Tiny islands and
coastlines can be absent/simplified at 1:50m, so city estimates are never arbitrarily
snapped to land. Country representatives convey **only the country**, not a host's
city. Flow details distinguish `dbip-city-lite` from `country-representative`.

IP geolocation can reflect an ISP registration, VPN exit, CDN or anycast designation.
It cannot prove which physical machine handled a connection, and a Lite estimate may
still be wrong. Lines illustrate connections, never the physical route. One selected
local site represents the map; this is not yet automatic multi-site geography.

## Privacy, attribution and updates

No extra traffic-IP requests or payload storage are introduced. The image-build
download exposes only the build machine's connection to the dataset server. DB-IP
Lite is CC BY 4.0; an attribution link is displayed on the map in both capture modes,
and `public/maps/DB-IP-NOTICE.md` is copied into the API image. Natural Earth is public
domain; world-atlas is ISC. Sources/schema:

- https://db-ip.com/db/lite.php
- https://db-ip.com/db/format/ip-to-city-lite/mmdb.html
- https://creativecommons.org/licenses/by/4.0/
- `public/maps/NOTICE.md` documents the pinned Natural Earth source and generator.

Updates are deliberate image rebuilds: update the dataset URL/checksum in Dockerfile.api
and the attribution date, then run the real-MMDB tests. No automatic runtime download
or API key is required. Missing/corrupt City Lite produces a backend warning and
country-only fallback where a country is already known. Standalone API development
can set `NERVE_CITY_DATABASE` to a local MMDB path; standard Docker setup requires no
new configuration. `NERVE_COUNTRY_DATABASE` compatibility remains unchanged.

## Validation and manual acceptance

Run Go unit/integration/vet tests, `npm run test:map`, `npm run typecheck`, production
build and `scripts/test-windows-capture.ps1`. Set `NERVE_TEST_CITY_DATABASE` to the
decompressed pinned MMDB to exercise actual IPv4/IPv6 database decoding in Go tests.
The browser test uses an isolated context and explicitly supplied test coordinates;
those coordinates are not defaults or claims about the host's location.

After `start.bat` rebuilds the images, open Map. An old site should be unset with its
candidate available under Local site. Save your coordinates or consciously select
Browser, inspect source/accuracy in Diagnostics, then reload. Generate real HTTPS
traffic, inspect a remote point's city/region and location source, and compare dark,
light and mobile views. Milestone 5 remains open until the user's visual acceptance.
