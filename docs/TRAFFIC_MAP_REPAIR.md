# Traffic Map: repair report

> Historical engineering log (September 2026). It records what was tested at the time
> and is not maintained. Current behaviour: [README](../README.md),
> [Traffic map](TRAFFIC_MAP.md), [Traffic sensor setup](TRAFFIC_SENSOR_SETUP.md),
> [Windows sensor](WINDOWS_TRAFFIC_SENSOR.md), [Geolocation](TRAFFIC_GEOLOCATION.md),
> [3D view](TRAFFIC_MAP_3D.md).

## Addendum: sensor setup, 2026-09-12

This follow-up work started from commit `278a050` on `milestone-5-map`. The repaired map
projection, flow animation, geolocation and REST v2 parsing were kept. The remaining
problem was the silent assumption of an already installed ntopng source and missing
information about capture visibility.

The architecture separates Windows discovery, the NERVE services and the capture sensor.
Docker Desktop does not expose a Windows NIC to libpcap, so the normal Compose
configuration does not start a misleading ntopng capture container. `setup.bat` offers an
external source or a fully prepared package for a native Linux sensor; credentials are
generated securely and kept in sync between sensor and API. Existing `.env` values,
comments and secrets are preserved. Diagnostics show the capture mode, the interface and
the explicitly limited visibility. A single ntopng interface can be resolved automatically
from the real REST v2 interface list; with several interfaces an explicit ID is required.

Verified successfully:

- Go unit tests, `go vet ./...` and integration tests against an isolated PostgreSQL,
  including the existing discovery flows and new diagnostics and leak checks.
- `npm run typecheck`, `npm run test:map`, production build including the static
  runtime check.
- Windows PowerShell 5.1: new `.env`, repeated run, secret persistence, external source,
  literal password, failure without changing `.env`, preservation of custom values.
- Compose validation of the standard installation and of the generated sensor package.
- Real Docker builds: API, web, Linux sensor and Windows discovery agent.
- Real ntopng 7.0.260911 in an isolated Docker network: account bootstrap, reader login,
  rejection of the default password, authentication failures, health check, a real HTTPS
  connection carried as a flow through to the NERVE API, no secrets in diagnostics or logs.
- Fresh, separate Compose installation with bootstrap and sign-in, correct missing-sensor
  state, repeated setup and service recreation with session and database retained. Only
  the test's own resources were removed afterwards.
- The sensor's production entry point explicitly rejects the actual WSL2 kernel.

The Docker runtime was outside `PATH` and was found under the local Docker Desktop
installation. The initially assumed ntopng tag `stable` does not exist; the official image
actually available is now pinned by digest. The real container test also revealed the
data directory permissions the restricted ntopng user needs; these were corrected.

Not demonstrated: physical Linux NIC, SPAN or TAP capture, Windows host capture, full LAN
monitoring, ARM sensor operation, and a manual double-click run of `start.bat` including
the start of the native discovery agent. The isolated container test explicitly covers
only the traffic of its own network namespace. The earlier visual map check below is not
a new visual browser test of this follow-up work.

Setup, visibility matrix, ports, privacy, diagnostic commands and manual capture
acceptance: [Traffic sensor setup](TRAFFIC_SENSOR_SETUP.md).

---

## ntopng repair, 2026-09-11

State as of 2026-09-11, branch `milestone-5-map`. No branch switch, push, PR or merge. The
changes and untracked traffic files present at the start were worked on further.

### Findings

- The local `.env` contained no ntopng keys. Compose, `.env.example` and the backend
  already knew them; `setup.bat` did not generate them and lost them on reconfiguration.
- There was no map library: continents and country borders were hand-drawn SVG paths. The
  projection height of 100 did not match the SVG height of 55, and the inverse click
  position was inconsistent as well.
- Frankfurt was set as the origin without a user decision. Location values were only
  checked for being finite numbers, not for being valid coordinates.
- Malformed responses and reachability failures were merged. A missing JSON structure
  could look like a successful empty response. Login redirects were not detected
  explicitly.
- Only one page of at most 500 ntopng flows was read and then truncated to 150 groups.
  Aggregated sessions showed only the ports of a single session.
- The country mapping covered only 25 countries. Error boxes were placed outside the map;
  dashed lines were the only animation.

### Implementation

The existing page, session authentication and REST integration were kept. D3 Geo and
topojson-client project locally bundled Natural Earth 1:50m countries. The map file is
about 756 KB; it is loaded and processed once per page load. A separate canvas layer uses
stable paths and incremental flow updates with fade-in and fade-out, a moving head and a
short tail, throughput-based stroke width and distinct direction colours. The date line is
split. Light and dark mode, reduced motion, filters and compact diagnostics are handled.

Locations are set explicitly, manually or through optional browser geolocation, and
stored locally. There is no default position. Validation, clearing and errors when browser
storage is unavailable are handled.

The ntopng client reads the documented `flow/active.lua` response, validates its
structure, handles login redirects separately and bounds pagination, response size and
time budget. Byte counters are read as integers in the backend. Country information comes
from the flows or optionally from `host/active.lua`. Aggregated sessions keep all ports,
counts, totals and stable group IDs. The map lists flows that cannot be placed and marks
partial results and stale statistics.

`nerve-server traffic-check` checks the same configuration inside the running API
container without database access. It prints status and counts, no credentials or flow IP
addresses.

### Configuration and manual acceptance

The complete example configuration, Docker commands, direct ntopng REST queries,
diagnostic states and the manual acceptance procedure are in
[TRAFFIC_MAP.md](TRAFFIC_MAP.md). The six variables are:

- `NERVE_NTOPNG_URL`
- `NERVE_NTOPNG_USERNAME`
- `NERVE_NTOPNG_PASSWORD`
- `NERVE_NTOPNG_INTERFACE_ID` (default `0`; choose the actual interface ID)
- `NERVE_NTOPNG_TIMEOUT` (default `5s`)
- `NERVE_TRAFFIC_LOCAL_CIDRS` (optional)

After changing `.env`, the API container must be recreated. `localhost` inside it refers
to the container; for ntopng on the Docker Desktop host, `host.docker.internal` is
usually required. Username, password, URL and interface must match the real ntopng.

### Checks

| Check | Result |
| --- | --- |
| `go test ./...` | Passed with Go 1.27.1, Windows/amd64 |
| `go vet ./...` | Passed |
| `go test -tags=integration -count=1 ./...` | Passed against a temporary PostgreSQL 18.4; no skipped database tests |
| Additional ntopng tests | Login redirect, 401/403, 5xx, invalid responses, empty response, pagination, timeout, ports, integer bytes and cache passed |
| Additional API integration tests | Distinct diagnostic codes and session protection passed |
| `npm run typecheck` | Passed |
| `npm run test:map` | Projection round trip, arc endpoints, date line and location validation passed |
| `npm run build` | Passed, including the existing static runtime check |
| Setup reconfiguration | Isolated test: all six values, including literal `$` and `#` password characters, are preserved |
| Browser | Dark and light map, location marker, persistence after reload, compact message and diagnostics panel checked against the real NERVE API |
| Mobile view | Checked at 390 px; whitespace found there was then reduced in CSS and the build re-checked |
| Docker builds | Invoked, not executable: Docker was not installed on this machine |
| 500-flow browser load test | Test harness prepared; the browser run was refused by the automatic approval check because of a usage limit; no FPS claim validated |
| Real ntopng instance | Not connected: no ntopng credentials were available here; API contract checked with HTTP test servers |

The temporary database of the last full test run was stopped cleanly. The test tools live
in the ignored `.nerve` directory; the application contains no demo flow provider.

### Limits of the ntopng repair at the time

- The target position is an approximate country centroid, not precise host geolocation.
- Inbound and outbound describe the session initiator; ntopng reports total bidirectional
  session throughput here. The statistics are not separate upload and download meters.
- At most ten pages of 500 source flows and 500 displayed groups; partial results are
  marked. Paging a live source is not an atomic snapshot.
- Without visible internal addresses (for example when NAT is observed only on the WAN),
  additional local CIDRs or a better-suited capture interface are needed.
- One location per browser; no multi-site mapping and no historical flow database.
- PostgreSQL integration was checked on 18.4 here, while the Compose deployment uses
  PostgreSQL 17. Docker and real ntopng acceptance therefore remained necessary.

## Native Windows extension, 2026-09-15 — acceptance pending

The previous limit was the missing Windows host source. Added were a separate
`nerve-sensor.exe`, the official Windows networking ETW manifest provider, a dedicated
HMAC-authenticated loopback ingestion endpoint, and the existing `traffic.Source`
abstraction as the shared map contract. Discovery and ntopng were not replaced. Details:
[Windows traffic sensor](WINDOWS_TRAFFIC_SENSOR.md).

Setup generates a separate 256-bit sensor token and picks a free loopback port
automatically. Existing database and discovery secrets and external sources are
preserved. Only the old unconfigured Windows default is migrated, once; a later deliberate
opt-out is kept. Stock launchers are updated selectively, custom launchers are kept. Start
manages a protected LocalService service; unchanged starts need no reinstallation. A
declined UAC installation stops neither Core nor discovery.

The normal kernel system logger was actually rejected under LocalService with
`0x80070005`. The Windows manifest provider, by contrast, worked under the same restricted
account and delivered real TCP and UDP, IPv4 and IPv6 flows from the detected Realtek
Ethernet adapter. No third-party capture driver was needed. Unneeded proprietary
DIA/KernelTraceControl helpers from the NuGet package are excluded from the final
artifact; licence notices and hash lockfiles are included.

The controlled HTTPS test then revealed a bug with TCP receive events: the mapping must not
assume that their tuple is always ordered in packet direction. The code now determines the
local side from the actual NIC addresses and the byte direction independently from the
event type. The added unit test passes. **Real acceptance of this last fix is pending:**
Windows aborted the required service update with "The operation was cancelled by the
user". The fix and the cleaned-up final artifact have therefore not yet been verified end
to end as a Windows service.

| Current check | Actual result |
| --- | --- |
| Go unit tests and `go vet ./...` | Passed |
| Go integration tests with a real isolated PostgreSQL | Passed |
| HMAC, time window, replay, input validation, offline/recovery and geo ranges | Unit tests passed; synthetic inputs, no capture evidence |
| Native `setup.bat` without input, path with spaces and repeated run | Passed; existing values and secrets are preserved |
| One-time migration and later capture opt-out | Passed |
| `npm run typecheck`, `npm run test:map`, `npm run build` | Passed |
| Compose configuration and loopback/frontend isolation | Passed |
| API, web, discovery, Windows sensor and Linux sensor Docker builds | Passed |
| Real Compose installation, sign-in and session after recreation, database persistence | Passed against PostgreSQL 17 |
| Real Windows start without a traffic source | Passed: Core and the native discovery agent run; INFO instead of abort |
| Real ntopng REST acceptance in an isolated container | Passed: real HTTPS flows, reader access, wrong password and default-password rejection |
| Windows ETW as LocalService | Real host flows demonstrated; no full TCP inbound acceptance of the final fix |
| Final Windows download, browser and double-start acceptance | Not completed because of the aborted UAC update; browser harness prepared, not yet run successfully |
| 500 active flows / sustained FPS acceptance | Not measured; states and output are bounded, no performance claim |

The native start tests suppress only the final cosmetic console pause with
`NERVE_NO_PAUSE=1`; Docker, discovery, the browser start command and the Windows service
calls are real. The older focused CMD test double remains an additional control-flow
test. The ntopng lab does not demonstrate SPAN or LAN coverage.

Isolated test services and containers were stopped; the existing personal installation
and its database were not reset. The demand-start test service stays installed and
stopped. Test configuration, transfer metadata and logs remain only under the ignored
`.nerve` directory.

Next acceptance step: run `scripts/test-windows-capture.ps1` again and confirm the Windows
UAC update. After that, both transfer directions, bytes, changing values, the real map,
failure and recovery, double start and stop must be verified successfully. **Milestone 5
is explicitly not complete at this point.**

## Launcher upgrade regression — 2026-09-15

The migration in `d3ce9c2` recognized only one predecessor hash per launcher.
The actual local `start.bat` matched official commit `278a050`, so setup wrongly
preserved it as custom. That launcher skipped the native sensor entirely.

Setup now uses a bundled, provenance-backed catalog of all 14 distinct official
historical launcher contents and a schema-versioned local installed-content baseline
in `.nerve/launchers.json`. Future upgrades compare against that baseline; edited
scripts are retained with an INFO message. No marker-only overwrite or Git runtime
dependency is introduced. Custom scripts also remain protected when the compatibility
`-RefreshLaunchers` switch is passed. Templates, capture implementation, discovery,
environment-generation logic and database lifecycle were not changed.

Executed successfully on Windows:

- `scripts/test-setup-bat.ps1`: real CMD/PowerShell file invocation without input,
  paths with spaces, occupied-port fallback, second setup, retained secrets/values,
  actual `278a050` upgrade, future baseline upgrade and two kinds of custom launcher.
- `scripts/test-launcher-history.ps1`: all 14 catalog entries against their Git
  provenance, actual migration, newline normalization and malformed-state fallback.
- `scripts/test-setup.ps1`: configuration, external/Linux source and secret persistence.
- `scripts/test-compose.ps1`: Compose validation and configuration isolation.
- `scripts/test-start-traffic.ps1`: focused CMD completion test with CLI/browser doubles.
- `scripts/test-windows-capture.ps1`: actual legacy launchers seeded before normal
  setup; no manual refresh after setup. Docker builds, native startup and protected
  sensor installation/update completed. Real Ethernet ETW HTTPS traffic matched curl
  endpoints and inbound/outbound byte checks. Flow changes, offline TTL, recovery,
  second start without duplicate sensor, discovery and retained session passed.
  Launcher `traffic-check` returned `connected`. Real Edge login/map tests passed
  with mapped real flows, stored manual location and dark/light/mobile screenshots.
  Stop and isolated cleanup passed; no user database was removed.
- Normal workspace `cmd.exe /d /c setup.bat` upgraded the actual old local launcher;
  `.env` remained byte-identical. `git diff --check` passed.

The first sandboxed Compose invocation could not locate Docker; the explicit local
Docker executable succeeded with authorized access. Go/frontend source was unchanged;
their standalone unit/vet/typecheck suites were not repeated for this launcher fix.
The Docker production builds and real browser test above did run. This is automated
native Windows acceptance evidence, not a claim that the user's final Explorer
double-click acceptance is complete. Milestone 5 remains open pending that acceptance.

## Geography and local-site provenance — 2026-09-15

The old map restored an unversioned localStorage coordinate pair solely because its
numbers were in range. No source/time existed, so the reported Atlantic value could
not be authenticated or attributed to a particular historical user/browser action.
There is no matching hardcoded default in the inspected source/history. The fix retains
legacy coordinates as review candidates and requires explicit confirmation before
drawing an origin. New records include provenance, timestamp and browser accuracy;
manual locations persist and browser fixes are only requested by an explicit action.

Both native Windows and ntopng flow snapshots now receive local DB-IP City Lite MMDB
enrichment. Country-only fallbacks use 235 Natural Earth representatives, all verified
inside their own rendered country geometry. Unknown geography stays unlocated/listed.
City/region estimates are unchanged, approximate and labelled; lines never claim a
physical route. No extra traffic-IP requests or payload storage were introduced.

Validation performed:

- `go test ./...` and `go vet ./...` passed, with `NERVE_TEST_CITY_DATABASE` pointing
  to the actual pinned September MMDB. Real IPv4/IPv6 decoding, invalid database,
  city/region selection, country-only fallback, unknown location, source immutability,
  stable IDs/counts and empty-array serialization are covered.
- Go integration tests (`go test -tags=integration -count=1 ./...`) passed against an
  isolated PostgreSQL instance, including existing auth/discovery/ntopng/sensor tests.
- `npm run typecheck`, `npm run test:map`, `npm run build` passed. All country points
  are tested against bundled Natural Earth geometry; local provenance/migration tests
  cover absent, invalid, unknown-source, legacy, manual, stale-browser and unset data.
- `scripts/test-setup-bat.ps1`, `scripts/test-setup.ps1`, `scripts/test-compose.ps1`
  passed. An initial sandboxed batch test could not atomically replace a test file;
  the authorized normal-access rerun passed without changing setup implementation.
- `scripts/test-windows-capture.ps1` exercised actual Docker API/web/discovery/sensor
  builds and native setup/start, real ETW HTTPS flows, live City Lite results, sensor
  offline/recovery and duplicate-start protection. Real Edge tests covered absent and
  Atlantic/invalid legacy sites, explicit browser estimation, manual persistence,
  dark/light/mobile screenshots, discovery and retained authentication. No traffic
  route mocks were used. Browser geolocation coordinates were explicitly supplied by
  the isolated test context, not claimed as the user's physical location.

See [TRAFFIC_GEOLOCATION.md](TRAFFIC_GEOLOCATION.md) for sources, CC BY 4.0 attribution,
build-time updates, memory cost, migration and remaining limits. Automated validation
does not close Milestone 5; the user's real visual acceptance remains outstanding.
