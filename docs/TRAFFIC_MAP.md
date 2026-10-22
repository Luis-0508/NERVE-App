# Live traffic map (Windows native / ntopng)

**Windows capture and automatic setup:** see [Traffic sensor setup](TRAFFIC_SENSOR_SETUP.md).
A fresh Windows installation enables a separate native ETW traffic sensor. Docker
Desktop does not capture the host NIC: `nerve-sensor.exe` runs on Windows and sends
real TCP/UDP endpoint, direction, byte and timing metadata to Core. No additional
Linux machine, ntopng installation or packet driver is required. The selected
physical default-route adapter appears in Map → Diagnostics.

Capture covers this host on the selected adapter's local addresses. The world map
shows external peers; LAN-wide monitoring still requires appropriate agents,
gateway capture, SPAN or TAP. Set your local site coordinates (manual or optional
browser location) once so arcs have an honest origin.

## External ntopng configuration

`traffic-sensor-setup.bat` remains optional for external ntopng and the separate
Linux sensor. Existing external/Linux configurations are preserved. Manual `.env` editing
is optional. Compose passes the following settings only to the API container. The Go binary reads process
environment variables, not the .env file directly. No credentials use NEXT_PUBLIC_ variables.

```dotenv
NERVE_TRAFFIC_CAPTURE_MODE=external
# Base URL reachable FROM the API container; no /lua/rest/... suffix.
# Docker Desktop host: http://host.docker.internal:3001 (adjust to your ntopng port).
NERVE_NTOPNG_URL=https://ntopng.example.lan:3001
NERVE_NTOPNG_USERNAME=nerve-reader
NERVE_NTOPNG_PASSWORD='replace-with-your-ntopng-password'
# auto selects exactly one interface. Multiple interfaces require an explicit ID.
NERVE_NTOPNG_INTERFACE_ID=auto
# Budget for the whole snapshot, including pagination and country enrichment.
NERVE_NTOPNG_TIMEOUT=5s
# Extra local public/CGNAT/VPN ranges; private IPv4 and IPv6 ULA are automatic.
NERVE_TRAFFIC_LOCAL_CIDRS=100.64.0.0/10,fd00::/8
```

Use single quotes for passwords containing $ or # (Compose otherwise interpolates
unquoted values). Use a trusted TLS certificate for HTTPS. localhost inside the API
container is the API container, not the Windows host. A reverse-proxy path prefix is
supported; configure its final URL because redirects other than login are rejected.
The ntopng user must have permission to read the selected interface.

After changing traffic configuration, rebuild if needed and recreate the API:

```sh
docker compose build api web
docker compose up -d
docker compose up -d --force-recreate api
docker compose exec api /app/nerve-server traffic-check
```

`traffic-check` uses exactly the running API container's environment and network,
requires no shell in the distroless image, and prints status, counts and capture metadata. It does
not print credentials, source URLs, or traffic IPs. An error exits with code 1;
connected/no-active-external-flows exits with code 0. A native process can run
`nerve-server traffic-check` after exporting the same environment variables.
`setup.bat` preserves existing values, comments, custom keys and secrets on repeated
runs. `start.bat` lets Compose load the values and runs a non-blocking traffic check
after startup; a missing sensor does not prevent NERVE or native discovery from running.

## Verifying ntopng directly

Use ntopng's documented endpoints (curl prompts for the password):

```sh
curl --user nerve-reader 'https://ntopng.example.lan:3001/lua/rest/v2/get/ntopng/interfaces.lua'
curl --user nerve-reader 'https://ntopng.example.lan:3001/lua/rest/v2/get/flow/active.lua?ifid=0&perPage=10'
```

References: [ntopng REST v2 examples](https://www.ntop.org/guides/ntopng/api/rest/examples_v2.html)
and [active.lua implementation](https://github.com/ntop/ntopng/blob/dev/scripts/lua/rest/v2/get/flow/active.lua).
NERVE parses active.lua (not the different active_list.lua schema): client/server IP
and port, protocol.l4/l7, integer bytes, thpt.bps, and first/last_seen. thpt.bps is
bits per second and is divided by eight internally. The UI formats throughput as bit/s.

## Diagnostics

| Diagnostics status | Meaning / action |
| --- | --- |
| capture_unavailable | Native service has not reported for 10 seconds; run start.bat and inspect Map → Diagnostics. Core/discovery remain usable. |
| not_configured | External ntopng source or credentials are missing. Optional setup: traffic-sensor-setup.bat. |
| authentication_failed | HTTP 401/403 or redirect to ntopng login; verify account and interface permissions. |
| unreachable | Network/TLS/timeout failure or upstream 5xx; check from the API container. |
| invalid_response | Wrong route/interface, nonzero rc, HTML, or an incompatible/malformed JSON schema. |
| invalid_configuration | Malformed base URL/ID, or auto with zero/multiple interfaces. Choose the actual interface ID. |
| no_active_external_flows | Successful response, but no sampled local-to-external sessions. |

The authenticated `GET /api/v1/traffic/live` endpoint returns distinct error codes.
Map → Diagnostics shows the code, last success, sampled counts, and unmapped counts.
On failure, the last statistics are visibly stale and old arcs fade out. An empty
success, unmatched filters, and flows without geography have separate messages.

## Map implementation and limits

This covers the World view and the shared snapshot; the 3D view is described in
[TRAFFIC_MAP_3D.md](TRAFFIC_MAP_3D.md).

- Natural Earth 1:50m countries are bundled in `public/maps`, rendered once with
  d3-geo Natural Earth projection and topojson-client. No CDN, tiles, street map,
  external geocoding, or invented polygons. Attribution/license: `public/maps/NOTICE.md`.
- Public remote IPs from both Windows and ntopng use locally bundled DB-IP City
  Lite (September 2026) city/region estimates. No IP lookup calls an external service.
  Country-only results use Natural Earth representatives verified inside the rendered
  country's land geometry. Unknown locations remain unplaced but inspectable.
  Locations are approximate, never verified physical server positions; arcs do not
  describe actual routing. See [geolocation details](TRAFFIC_GEOLOCATION.md).
- For ntopng, inbound/outbound means **session initiation**: remote client → local server, or
  local client → remote server. The endpoint supplies total bidirectional session
  throughput, so the two statistics are not independent upload/download meters.
- Set Local site manually, or explicitly request browser geolocation. There is no
  assumed location. Versioned coordinates, source and time persist in localStorage.
  Old unversioned values require explicit review and confirmation; they are retained
  as candidates instead of being silently used or deleted. Browser estimates include
  reported accuracy and require confirmation on reload after 30 days. Manual locations persist.
  Browser geolocation generally requires HTTPS or localhost and permission; manual
  entry also works on an HTTP LAN address. One local site is used for the whole map.
- Stable groups combine local IP, remote IP, L4/L7 protocol and session direction.
  All contributing local/remote ports and session counts are retained. First/last
  seen span the active sessions in a group; there is no historical retention.
- The browser polls every two seconds without overlapping requests, pauses polling
  in hidden tabs, and supports Pause/Refresh. Shared backend results/errors are
  cached for 1.5 seconds. Up to ten pages of 500 source rows and 500 displayed groups
  bound the work. Partial snapshots are labelled; totals describe sampled external
  flows. Active ntopng pages are not an atomic historical snapshot.
- Canvas paths survive throughput updates. New/old paths fade, moving heads and tails
  follow curved routes in session direction; width follows throughput. The date line
  is split, never joined across the whole map. Animation targets 30 FPS, caps DPR at
  two, and respects reduced motion. Static cartography is not regenerated on polls.
- NERVE ships no demo flow source. Automated tests use isolated fixtures.

## Manual acceptance test

1. Configure ntopng, recreate the API, and run traffic-check. Select the interface
   actually observing the local subnet; NAT-only observation may hide internal hosts.
2. Sign in, open Map, enter your site coordinates, reload, and check the saved marker.
   Clear it and test manual entry and optional browser geolocation separately.
3. Generate real external traffic on a monitored device. Compare IPs, all ports,
   protocol/application, bytes and throughput in the inspector against ntopng.
4. Exercise direction/application/protocol/device/search filters, Pause, Refresh,
   appearance switching, and narrow windows. Use the flow list for overlapping dots.
5. Close sessions and wait for ntopng to expire them: paths fade out; new sessions
   fade in. Refresh should not replace the world geography.
6. Test missing configuration, wrong password, unreachable URL, wrong interface,
   zero external flows, and missing country data. Diagnostics must distinguish them.

The validation commands are listed in the [README](../README.md#development).
