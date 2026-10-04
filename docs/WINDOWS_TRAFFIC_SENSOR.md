# Native Windows traffic sensor

The sensor shipped with the Traffic Map (PR #21). A passing build or mocked flow test
is not a Windows capture acceptance result; use the acceptance steps below on a real
host. Historical test records are in [TRAFFIC_MAP_REPAIR.md](TRAFFIC_MAP_REPAIR.md).

## Normal installation

1. Start Docker Desktop (Linux containers).
2. Double-click `setup.bat`: no technical input is requested. It chooses a free web
   port (preferring 3000), generates independent 256-bit database, discovery and
   sensor secrets, and chooses a free loopback ingestion port. No driver is installed.
3. Double-click `start.bat`. It builds Core, discovery and the self-contained Windows
   sensor. The first protected Windows service installation requires UAC. A changed
   binary/configuration needs a new authorized update; unchanged starts do not.
4. Sign in, open Map and set Local site (manual coordinates or optional browser
   geolocation). No random default location is substituted.

Existing database passwords, discovery tokens, native tokens, ports and custom
values are retained. An unconfigured prior `sensor-required` Windows default is
upgraded to `windows-native`; explicit external/Linux configurations remain intact.
Known previous stock launchers upgrade automatically. Custom launchers are kept:
merge the sensor calls from `start.example.bat`/`stop.example.bat` after reviewing them.
Setup reports preserved custom launchers explicitly. `-RefreshLaunchers` remains
accepted for compatibility but no longer bypasses customization protection.
Moving the repository creates a different installation identity; stop the old
installation first. Nothing here deletes PostgreSQL or sensor volumes.

## Launcher upgrades

Normal `setup.bat` identifies legacy stock launchers by exact SHA-256 content
identity against `scripts/stock-launchers.json`, including provenance from the
official Git history. CRLF/LF and final newline differences are ignored; arbitrary
content changes are not. No banner, filename or version comment alone authorizes
replacement. This also works in ZIP distributions without Git installed.

After installation, ignored `.nerve/launchers.json` (schema 1) records the installed
content version of each launcher. Subsequent releases compare against that baseline
before copying their current template. This avoids a new hardcoded predecessor hash
for every release while detecting user edits. Missing/corrupt metadata falls back
to the catalog; unknown scripts are preserved with an explicit INFO message. Keep
this state when upgrading. It contains no credentials. Setup never needs to touch
database containers or volumes to migrate launchers.

Maintainers: run `powershell.exe -NoProfile -File scripts/update-stock-launchers.ps1`
from the full official branch history before releasing launcher changes, review the
catalog diff, and commit it. The generator retains existing historical entries.
Run `scripts/test-launcher-history.ps1` (requires full history) and
`scripts/test-setup-bat.ps1` (also works without Git). The latter starts native CMD
without input, including the actual affected `278a050` fixtures and custom scripts.

## Capture architecture

`nerve-sensor.exe` is separate from the unprivileged Go discovery agent. ETW session
rights, transport-event load and capture failures cannot interrupt discovery jobs.
The Windows backend uses the built-in `Microsoft-Windows-Kernel-Network` manifest
provider, GUID `7dd42a49-5329-4832-8dfd-43d979153a88`, IPv4/IPv6 keywords `0x30`.
Microsoft's MIT-licensed TraceEvent library consumes the real-time events. Only
TCP/UDP send/receive events contribute bytes: no retransmit/copy double-counting,
no packet payload recording, no injection, no Npcap/WinDivert or third-party driver.

The provider's version-0 schema can be inspected on the actual Windows machine:

```powershell
wevtutil gp Microsoft-Windows-Kernel-Network /ge:true /gm:true
.\.nerve\sensor-build\nerve-sensor.exe --schema
.\.nerve\sensor-build\nerve-sensor.exe --interfaces
```

The official .NET SDK Docker image is pinned by digest. NuGet restore uses only
nuget.org, exact lockfile versions and integrity hashes. The published Windows x64
binary includes its runtime, so neither a local .NET SDK nor Go installation is
required. License notices are shipped with the service. Future binary releases
should additionally be publisher-signed; this repository currently builds locally.

## Scope and flow semantics

The sensor requires an active IPv4 default route and selects an up Ethernet/Wi-Fi
adapter before recognizable virtual/VPN adapters, then the lowest default-route
metric, then a stable adapter ID. ETW observes the host stack; attribution filters
to the chosen adapter's current local IPv4 and IPv6 addresses. Adapter/address
changes trigger reselection. This is not promiscuous physical-interface capture.
VPN routing/encapsulation can hide the inner path or make it fail this address
filter; IPv6-only hosts and Windows ARM64 are not supported by this build.

Each direction has a real five-tuple (local/remote IP and port, protocol), observed
byte totals, first/last event times and a two-second delta throughput. An inbound
download is receive traffic even when this host initiated its TCP connection.
ntopng retains its existing **session initiator** direction and bidirectional
session totals; the map labels that distinction. ETW bytes are TCP/UDP event sizes,
not an exact Ethernet wire-byte counter. Payload/application names are not guessed;
native application is `Unknown`. PID/process enrichment is not implemented.

Flows idle for 15 seconds expire. Sensor state is capped at 5,000 directional
five-tuples; API aggregation produces at most 500 stable destination groups. The
map updates its existing canvas incrementally. Overflow or ETW event loss marks a
partial snapshot. This does not promise lossless capture at arbitrary line rates.

The API filters local/multicast peers from the world map and resolves public IPv4/
IPv6 addresses using locally bundled DB-IP City Lite. Country-only results use
Natural Earth representatives verified inside the corresponding country geometry.
No per-IP web lookup occurs. Geography is approximate; unknown locations stay
unlocated. DB-IP attribution appears on the map. See [geolocation](TRAFFIC_GEOLOCATION.md)
for local-site migration, accuracy, offline data updates and licensing.

| Traffic | Visibility |
| --- | --- |
| Windows applications to/from Internet on selected host addresses | TCP/UDP transport events |
| Host-to-LAN TCP/UDP | Seen by backend; local peers excluded from world map |
| Docker/WSL containers | Not complete or separately attributed; only events exposed by Windows on selected addresses |
| Other LAN devices talking to each other/Internet | Not visible merely because they share the LAN |
| Complete LAN | Requires suitable agents, gateway capture, SPAN/TAP and topology/capacity coverage |

## Security and lifecycle

The Windows service runs as **LocalService**, not as an administrator or SYSTEM.
The manifest-based network provider works with this account; the system-kernel
logger was explicitly tested and rejected with access denied, so it is not used.
The service is demand-start and is stopped by `stop.bat`; no capture runs
automatically just because Windows boots. Its executable/runtime live under
`%ProgramFiles%\NERVE\NerveSensor-<installation-id>` with protected ACLs. Config is
readable only by SYSTEM, administrators and the capture service account. The
installing user can start/stop/query the service but cannot replace its binaries or
reconfigure it. Core and discovery retain their existing privilege model.

The separate `NERVE_SENSOR_TOKEN` is never a process argument or frontend value.
Reports use HMAC-SHA256 over `unix_seconds + newline + nonce + newline + raw_body`.
The API rejects bad signatures, stale timestamps, nonce replay, oversized reports,
invalid flows and browser-originated requests. Sensor ingress is a separate API
listener on container port 8091, published **only** as
`127.0.0.1:${NERVE_SENSOR_PORT}:8091`; Nginx does not route it. LAN clients cannot
impersonate a sensor through the web API. This is not protection against a local
administrator or a process that can read the owning user's `.env`.

Only volatile flow state is retained. Traffic metadata still identifies communication
partners; protect `.env`, machine access and backups. ETW sessions have bounded
buffers and deterministic per-installation names. The Windows service manager
prevents duplicate service processes; stop disposes the session. Recovery recreates
only this installation's own session, not a shared global kernel logger.

## Diagnostics

```powershell
docker compose exec -T api /app/nerve-server traffic-check
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/manage-traffic-sensor.ps1 -Action Status
Get-Service NerveSensor-*
```

The status command prints no token or flow IPs. Map → Diagnostics shows selected
interface, host-only coverage, last success and ETW loss/phase/error code. A missing
heartbeat expires after ten seconds (`traffic_capture_unavailable`); permission
and backend failures are separate. An empty connected sensor reports no active
external flows. None of these statuses marks the whole NERVE Core offline.

`traffic-sensor-setup.bat` still configures external ntopng or a native Linux sensor
at an appropriate observation point. See [Traffic sensor setup](TRAFFIC_SENSOR_SETUP.md).
The versioned normalized report protocol and `traffic.Source` interface allow a
future native Linux backend; this change does not implement multi-sensor merging.

## Acceptance

`scripts/test-windows-capture.ps1` runs the real batch files in an isolated path
with spaces and a separate Compose project/database/service. It requests real UAC,
downloads real HTTPS data on Windows, checks authenticated flows, updates, expiry,
restart, double start, discovery and stop. No generated flow fixtures enter that
test. It retains its isolated database/ignored evidence and stops test services;
the installed demand-start test service is not uninstalled on every stop.
Automated start/stop tests set `NERVE_NO_PAUSE=1` only to skip the final cosmetic
console-closing keypress; they do not skip startup, UAC, capture or health checks.
The executable's `--self-test` deliberately uses synthetic unit input and must not
be counted as a real capture result. Also run the Go, frontend and Compose tests
listed in the main traffic setup document.

If UAC is declined, Core/discovery still start and the map reports capture
unavailable. Run `start.bat` again when the protected installation can be approved.
Do not treat a connected service alone as accepted capture: compare the
controlled download's actual local/remote addresses, both directions and byte
counts, then verify the real browser map and recovery.
