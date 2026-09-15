# Traffic capture architecture and setup

## Decision

NERVE separates the management application, native discovery, and packet capture.
The normal Desktop Compose stack remains PostgreSQL + API + web. Adding a Linux
ntopng container there would not deliver Windows-host or LAN capture. An optional
**separate native Linux sensor deployment** is supplied under `deploy/sensor` and
prepared by `traffic-sensor-setup.bat`. Existing external ntopng sources continue to work.

Docker Desktop's Linux containers run behind its VM networking. Even Desktop host
networking provides layer-4 access, not direct Windows NIC access. Neither
`host.docker.internal`, WSL mirrored networking, nor promiscuous mode inside a
container is a replacement for physical packet delivery. See
[Docker host-network limitations](https://docs.docker.com/engine/network/drivers/host/)
and [Desktop networking](https://docs.docker.com/desktop/features/networking/networking-how-tos/).

Native ntopng on Windows needs additional installation/capture-driver configuration.
nTap Remote also requires Npcap on Windows, and its collector has license requirements:
[Windows ntopng](https://www.ntop.org/guides/ntopng/how_to_start/running_on_windows.html),
[nTap installation](https://www.ntop.org/guides/ntap/installation.html),
[nTap licensing](https://www.ntop.org/guides/ntap/license.html).
NERVE does not silently install kernel drivers or assume commercial licenses.

## Standard Windows installation

1. Install/start Docker Desktop with Linux containers; clone the repository.
2. Double-click `setup.bat`. It needs no input and closes automatically after success.
   A fresh installation gets secure random secrets and port 3000, or a free port
   selected by Windows when 3000 is unavailable. The port and both local URLs are
   stored consistently in `.env`. Existing installations keep their saved port.
3. Run `start.bat`. It builds/starts NERVE and its native Windows discovery agent.
   The final traffic check is informative and does not block discovery.

No sensor means no traffic data. The map displays the compact “Capture sensor
required” state and explains the next step in Diagnostics. Discovery still finds
reachable devices, neighbors and services; it does not measure flow throughput.
This prerequisite cannot be truthfully eliminated through Compose alone.

Normal setup only configures the local installation, generates cryptographically
random database/discovery secrets, and marks fresh capture as `sensor-required`.
There are no questions about ntopng, Linux, IP addresses or capture interfaces.
`traffic-sensor-setup.bat` is a separate optional step. Choose `e` for an existing
ntopng instance or `l` for a Linux sensor; Enter keeps the current source unchanged.
Password prompts do not echo input. The Linux sensor option creates independent
256-bit administrator and reader secrets,
writes a complete `.nerve/sensor` bundle, and points the API to its management
address. No manual ntopng account creation or `.env` editing is needed for this
option. The bundle must still be deployed at a suitable capture point.

Existing `.env` values, comments, additional keys and secrets are retained. Repeated
setup does not rotate valid secrets or recreate databases. `.env` updates use atomic
replacement. Existing custom launchers are retained without a prompt. To deliberately
refresh them from the current templates, run
`powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/setup.ps1 -RefreshLaunchers`.
The port check reflects availability at setup time; existing ports are never silently
changed on subsequent runs. Neither setup nor stop
removes database or sensor volumes.

## Optional Linux sensor

Run `traffic-sensor-setup.bat`, select `l`, and supply the Linux host's explicit management IPv4 address and
capture NIC name. Copy `.nerve/sensor` securely to that host and run
`docker compose up -d --build` there. See the generated README, also available in
[deploy/sensor/README.md](../deploy/sensor/README.md), for permissions and firewall setup.

The sensor uses native Linux host networking and one explicit interface. Redis
listens on a private container Unix socket, starts before ntopng, and stores sensor
configuration in its own volume. Generated accounts are installed before ntopng's
listener opens. The NERVE reader has the unprivileged role. Login is never disabled.
An authenticated REST-v2 healthcheck rejects a login-page response. The process
supervisor stops the service if Redis or ntopng exits; Compose restarts it.

TCP 3001 binds only the selected Linux management IP. It is needed for API access,
not published through Docker port mapping. Restrict access to the NERVE API egress
address. HTTP Basic auth requires a trusted management network/VPN or a TLS proxy
with a trusted certificate. No public Redis port is opened. The default NERVE web
port stays 3000. Generated credentials stay outside Git and never reach the browser.

The official ntopng image is pinned by digest, currently Linux amd64. Community mode
avoids trial-license behavior. Upgrades must revalidate the ntopng Redis account
schema, API health and real capture. ARM requires a separately validated image.
`--offline` and DNS mode 2 avoid ntopng's external update checks and active reverse
lookups. Country information still depends on available local GeoIP data; missing
geography is reported as unlocated, never invented. Normal ntopng local statistics
persist; packet recording and historical-flow exports are not enabled by NERVE.

## What is actually visible?

| Observation point | NERVE-container traffic | Windows host / Internet | Other LAN devices | Complete LAN |
| --- | --- | --- | --- | --- |
| Standard Windows setup, no sensor | No flow capture | No flow capture | Discovery only | No |
| ntopng in an ordinary Desktop container (not offered as production mode) | Only traffic delivered to that container/network namespace | No general capture | No general capture | No |
| Native Linux sensor on a normal switch port | Only if traffic traverses that NIC | Only traffic to/from that Linux host, not arbitrary Windows traffic | Delivered broadcast/multicast and traffic to/from sensor | No |
| Sensor at a gateway, SPAN destination or TAP | If it traverses the observation point | If mirrored/routed there | Only mirrored/routed links and directions | Only with adequate topology coverage and capture capacity |

SPAN must mirror relevant VLANs/ports and both directions. A gateway/uplink capture
does not see same-switch traffic that bypasses it. A mirror can drop packets when
oversubscribed. WAN-side NAT may remove local device identities. Wi-Fi client mode
does not promiscuously expose all stations' unicast traffic. Full LAN monitoring is
a network-design property, not a flag that NERVE can verify from an API connection.

The world map intentionally displays local-to-external sessions, not all captured
LAN-local flows. Session direction describes the initiator; total bytes/throughput
are bidirectional session totals. Site coordinates remain optional and browser-local.

## Diagnostics and acceptance test

On Windows, in the repository:

```powershell
docker compose ps
docker compose exec api /app/nerve-server traffic-check
```

On the Linux sensor, in its bundle:

```sh
ip -br link
docker compose ps
docker compose exec ntopng /opt/nerve/healthcheck.sh
docker compose logs --tail 80 ntopng
```

Map → Diagnostics distinguishes connected, missing configuration, authentication
failure, unreachable source, invalid response/configuration, and no active external
flows. It shows capture mode, selected interface ID (and resolved name for auto),
coverage limitations, last successful snapshot and sample counts. Configuration
metadata is declared coverage, not measured proof. No source URL or credential is
serialized to the browser. A stale snapshot is not presented as current traffic.

For acceptance, generate a controlled real download on (1) the sensor host,
(2) Windows, and (3) another LAN device. Inspect each source IP, destination,
ports/application and both directions in ntopng; compare NERVE's external flows.
Repeat after enabling/disabling SPAN to establish the visibility boundary. Verify
that local-local flows visible in ntopng need not appear on the world map. Record
the NIC, switch configuration, losses and NAT placement. An empty external-flow
list alone cannot distinguish an idle network from missing packet delivery.

## Security and extension limits

Traffic metadata reveals device IPs, services and communication partners. Use only
authorized capture points; protect sensor files, volumes, backups and management
access. Do not attach `.env`, secret files or interpolated `docker compose config`
output to bug reports. NERVE's CLI diagnostic prints only sanitized metadata/counts.
Sensor files are the authoritative passwords across restarts; rotate the file and
API password together, then recreate the sensor/API. Changing only the ntopng UI
password will be reverted by the managed bootstrap.

The code keeps one active source/interface per NERVE deployment. The separate sensor
boundary and explicit capture metadata support later sensor inventories and routing,
but this change does not claim multi-sensor aggregation. Multiple sensors need
explicit ownership, stable sensor IDs and duplicate-flow handling in a future change.

## Automated checks

Run the existing Go unit/integration tests, `go vet ./...`, `npm run typecheck`,
`npm run test:map`, and `npm run build`. Additional Windows PowerShell checks:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-setup.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-setup-bat.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-start-traffic.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-compose.ps1
docker compose --env-file .env.example build api web
docker build -t nerve-sensor:local-test deploy/sensor
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-sensor.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-installation.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-start-bat.ps1
```

The sensor test explicitly invokes its runtime inside a bridge-network lab container
to test Redis/accounts/REST and a real HTTPS download. It does not invoke native
capture mode or claim Windows-host visibility. The production entrypoint separately
rejects known Desktop/WSL kernels. Installation tests create a uniquely named Compose
project with a free web port and a test-only database, verify setup/recreation and
session persistence, and remove only their own containers/volume. Ignored fixtures
remain under `.nerve` and contain generated test secrets.
The native batch test supplies no stdin at all, checks occupied-port fallback and
retention of an existing traffic source. The start test invokes the actual launcher
with real Docker builds, an isolated Windows discovery agent and its browser-opening
command; its test agent and Compose project are removed afterwards.

For the setup-UX change, native batch tests ran without sending any input, including
port 3000 occupied/free, a path with spaces, an existing occupied configured port,
and unchanged secrets/traffic/custom values on rerun. Optional sensor setup and
Compose validation passed. The focused start-completion test passed using a CLI
test double returning the real missing-sensor exit code 1; the launcher still requests
the browser and exits 0. A full native `start.bat` run was attempted but stopped at
the Docker availability check because the local Engine did not become available.
That attempt does not establish a new Docker/Windows-agent end-to-end success.
