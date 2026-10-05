# NERVE

NERVE is a self-hosted network inventory, discovery and traffic-visualization platform. Its goal is a provenance-aware view of the network: devices, services and their relationships, plus what is observed versus what is derived.

## What NERVE does today

- **Sign-in, roles and permissions**: first-administrator setup, hashed server-side sessions, rate-limited login. Roles (`administrator`, `technician`, `read_only`) map to explicit server-side permissions stored in the database. An immutable audit log records authentication, discovery administration, agent lifecycle and remote commands.
- **Device inventory**: PostgreSQL-backed devices, interfaces, addresses, services and relationships, with server-side search, sort, filter and paging, and a device detail view.
- **Local discovery**: an administrator configures one subnet and starts scans. A native host agent (Windows or Linux) collects ICMP/TCP reachability, ARP/NDP neighbours and reverse DNS. Core reconciles devices by MAC first, then subnet/IP, and tracks online/offline state.
- **Live traffic**: a native Windows ETW traffic sensor (host traffic only) or an external ntopng source, exposed as a normalized, bounded snapshot with explicit error states.
- **Traffic Map**: a geographic **World** view, and a **Topology 3D** view that places inventory devices on derived /24 plates and routes measured flows through the local uplink to remote endpoints on a globe. See [3D view](docs/TRAFFIC_MAP_3D.md).
- **Remote management (foundation)**: per-host NERVE agents enroll with a single-use token and get their own Ed25519 identity, report heartbeats and capabilities, and are linked to inventory devices from evidence (operator-confirmable). Operators with the right permission can run five read-only commands (system info, metrics, processes, services, installed software) from the device page; every step is audited. Core signs its responses so agents can reject forged commands. Interactive sessions and privileged commands are not available. See [Remote management](docs/REMOTE_MANAGEMENT.md) and [Agent setup](docs/AGENT_SETUP.md).

Not available yet: editing device names, tags, notes or trust state; merge/split; multiple or scheduled discovery targets; observed local topology (LLDP/CDP/SNMP, switch ports, device-to-device links); Proxmox and Docker integrations; remote desktop, terminal, file transfer, shell or PowerShell commands and other privileged remote actions; releases and backup tooling. No screen uses static or demo data. The plan for these is in [IMPLEMENTATION_PLAN.md](./IMPLEMENTATION_PLAN.md), and the remote-management direction is in [docs/REMOTE_MANAGEMENT.md](docs/REMOTE_MANAGEMENT.md).

## Run with Docker Compose

Docker is the intended local and production development path.

1. Copy `.env.example` to `.env`.
2. Replace `POSTGRES_PASSWORD` with a strong local secret.
3. Set `NERVE_COOKIE_SECURE=true` when the public endpoint uses HTTPS.
4. Run `docker compose up --build`.
5. Open `http://localhost:3000/setup` and create the first administrator.

The setup endpoint closes itself after the first user is created. Subsequent setup requests return HTTP 409.

## Windows quick start

NERVE must be started as the complete Compose stack; `npm run dev` starts only the frontend and intentionally does not provide an `/api/v1` proxy.

1. Install and start Docker Desktop with the Compose v2 plugin.
2. Double-click `setup.bat`. No input is needed: it creates `.env` with cryptographically random database, discovery-agent and traffic-sensor secrets and the Core signing key for management agents, selects port 3000 (or a free port if occupied), and generates the ignored local `start.bat` and `stop.bat`. Existing configuration, ports, secrets and custom launchers are retained; official historical stock launchers are upgraded automatically. Launcher migration details and maintainer instructions: [Windows traffic sensor](docs/WINDOWS_TRAFFIC_SENSOR.md#launcher-upgrades). Successful setup closes automatically after a short completion message.
3. Run `start.bat` from Explorer, PowerShell, or Command Prompt. Both scripts always change to their own project directory first and keep the console open until a key is pressed.
4. The start script builds/starts Core, the Windows discovery agent and the native Windows traffic sensor. Accept the first sensor service installation UAC prompt; subsequent unchanged starts need no elevation. Sensor failures are reported separately and never stop Core/discovery. The browser opens after Core/discovery readiness.

Generated secrets stay in ignored local configuration; no batch file contains them. The sensor token is additionally copied to its ACL-protected service configuration. When an existing `.env` is reconfigured, `setup.bat` keeps its current database password because PostgreSQL does not reapply `POSTGRES_PASSWORD` to an initialized volume. Discovery and capture authenticate separately and never receive browser sessions or database credentials. Management agents use their own per-host keys (see [Agent setup](docs/AGENT_SETUP.md)).

**Transport.** The local quick start serves NERVE over plain HTTP. Browsers, the discovery agent and management agents talk to Core over whatever URL they are given, so traffic is encrypted only if you put NERVE behind an HTTPS reverse proxy with a trusted certificate. Management agents authenticate every request with their own key and verify Core's response signatures, so commands cannot be forged on the network, but on plain HTTP their results and inventory data are readable by anyone on the path. Use HTTPS for agents outside a trusted network.

Use `http://localhost:3000` unless `NERVE_WEB_PORT` was changed in `.env`. When a reliable active IPv4 address is available, the script also prints a LAN URL such as `http://192.168.1.25:3000`. Docker publishes the web proxy on all host interfaces. Windows Firewall and the local network must permit inbound access to the configured port.

The browser API remains reachable through the web reverse proxy. Native sensor ingestion uses a separate authenticated endpoint published only on host loopback; it is not exposed through Nginx. For LAN access, Nginx forwards the original host and scheme and the API accepts only an `Origin` that matches that trusted proxy origin or an explicitly configured `NERVE_ALLOWED_ORIGIN`; origin checks are not disabled. Keep `NERVE_COOKIE_SECURE=false` for plain local HTTP and set it to `true` when NERVE is served through HTTPS.

To stop NERVE, including the host discovery agent and traffic sensor, while preserving PostgreSQL data, run `stop.bat`. The Compose-only equivalent is:

```text
docker compose down
```

Use `docker compose down -v` only when the PostgreSQL volume and all local NERVE data should be deleted.

### Runtime verification

The following checks all use the same address exposed by the reverse proxy:

- `/` returns the web interface.
- `/api/v1` returns the NERVE API name, version, and `status: ok`.
- `/health/live` confirms that the Go process is running.
- `/health/ready` returns HTTP 200 only when the API can reach PostgreSQL.
- `/health/discovery` returns HTTP 200 only while a local discovery agent is reporting heartbeats.
- `/api/v1/status` reports both overall `status: ok` and `database.status: ok`.

`docker compose ps` shows the container state. Use `docker compose logs --tail 100 postgres api web` when a service is not ready. The start script prints both automatically after a failed startup and does not open a browser in that case.

### Local network discovery

Open **Devices**, enter a canonical subnet such as `192.168.178.0/24`, save it, and select **Scan now**. Only users with the `discovery.manage` permission (administrators by default) can configure or start scans, and every change is audited. The page shows whether the host agent is online, the current scan state, reachable/total counts when available, and recent scan errors. Results are written into the normalized device, interface, address, identity, observation, event, and discovery tables; the inventory refreshes when a scan completes.

On Windows with Docker Desktop, discovery intentionally runs as a native host process. Docker Desktop runs Linux containers inside a VM, and its host-network feature exposes TCP/UDP at layer 4 rather than the host's layer-2 interfaces. Running ARP/NDP discovery inside the API container would therefore inspect the wrong network namespace. `start.bat` cross-compiles the Go agent in Docker, writes the generated binary and logs beneath ignored `.nerve/`, and starts it with ordinary user privileges. ICMP uses the operating-system `ping` command; the agent then reads the host ARP/NDP neighbor cache, performs bounded parallel reverse DNS, and uses bounded TCP connection attempts as a fallback.

For a later Linux server or VM, build `Dockerfile.agent` with `TARGETOS=linux` and run the resulting `nerve-agent` directly on that host with `NERVE_AGENT_API_URL` and `NERVE_DISCOVERY_AGENT_TOKEN` set. This keeps discovery in the target network namespace without granting raw-socket or Docker-socket privileges to the Core container.

If a browser tab remains blank after rebuilding an older checkout, reload it without cache (`Ctrl+F5`). The production build includes `npm run test:runtime`, which verifies that the exported application shell, referenced static assets, and Nginx content-security policy are compatible with the Next.js bootstrap runtime.

## Local frontend development

Install Node.js 26 or use the production Docker build, then run:

```text
npm install
npm run dev
```

For a complete local session, expose the API through the same origin or use the included Compose proxy. The browser client intentionally uses same-origin `/api/v1` requests so privileged credentials and sessions are not distributed across origins in production.

## Local API development

Install Go 1.26 or newer and PostgreSQL 17, configure the variables shown in `.env.example`, then run:

```text
go mod download
go test ./...
go run ./cmd/nerve-server
```

The service applies ordered `.up.sql` migrations at startup while holding a PostgreSQL advisory lock. It rejects a migration whose checksum changes after application.

## Endpoints

- `GET /health/live` — process liveness
- `GET /health/ready` — PostgreSQL readiness
- `GET /health/discovery` — local discovery agent readiness
- `GET /api/v1` — API identity, version, and process status
- `GET /api/v1/status` — version, uptime, and database state
- `POST /api/v1/auth/bootstrap` — one-time administrator initialization
- `POST /api/v1/auth/login` — create an opaque, hashed server-side session
- `POST /api/v1/auth/logout` — close the current session
- `GET /api/v1/auth/session` — inspect the current session
- `GET /api/v1/events` — authenticated event history
- `GET /api/v1/devices` — authenticated device inventory; accepts `search`, `status`, `classification`, `sort`, `direction`, `limit`, and `offset`
- `GET /api/v1/devices/facets` — status and classification values present in the inventory
- `GET /api/v1/devices/{deviceID}` — composed device detail
- `GET /api/v1/devices/{deviceID}/interfaces` — device interfaces with assigned addresses
- `GET /api/v1/devices/{deviceID}/ip-addresses` — current and historical device addresses
- `GET /api/v1/devices/{deviceID}/services` — service endpoints hosted by the device
- `GET /api/v1/devices/{deviceID}/relationships` — inbound and outbound entity relationships
- `GET /api/v1/discovery` — authenticated target, agent, latest scan, and error state
- `POST /api/v1/discovery/target` — subnet configuration (`discovery.manage`, audited)
- `POST /api/v1/discovery/scans` — scan request or cancellation (`discovery.manage`, audited)
- `GET /api/v1/stream` — authenticated server-sent event stream
- `GET /api/v1/traffic/live` — authenticated, normalized Windows-native or ntopng v2 active-flow snapshot
- `POST /api/v1/discovery/agent/heartbeat`, `.../agent/jobs/claim`, `.../agent/jobs/{scanID}/report`, `.../agent/jobs/{scanID}/complete` — discovery-agent endpoints, authenticated by the agent token, never by browser sessions
- `GET /api/v1/audit` — immutable audit log; filters `targetType`, `targetId`, `action` (prefix), `limit` (`audit.view`)
- `GET /api/v1/agents`, `GET /api/v1/agents/{agentID}` — enrolled management agents with status, capabilities, key fingerprint and device association (`agents.view`)
- `POST /api/v1/agents/{agentID}/revoke`, `.../remove` — revoke the agent's credential (re-enrollable) or retire it permanently; pending commands are cancelled (`agents.manage`)
- `POST /api/v1/agents/{agentID}/association` — `confirm` a device, `dismiss` (no device) or `reevaluate` evidence (`agents.manage`)
- `GET /api/v1/agent-enrollments`, `POST /api/v1/agent-enrollments`, `POST /api/v1/agent-enrollments/{enrollmentID}/revoke` — single-use enrollment tokens, shown once at creation (`agents.manage`)
- `GET /api/v1/devices/{deviceID}/remote` — remote-management endpoints and capabilities for a device, across providers (`agents.view`)
- `GET /api/v1/devices/{deviceID}/commands`, `GET /api/v1/agents/{agentID}/commands` — command history without output (`remote.view`)
- `POST /api/v1/agents/{agentID}/commands` — queue a read-only command (`remote.execute.read_only`); other command types are refused
- `GET /api/v1/remote/commands/{commandID}` — command state and retained result (`remote.view`); `POST .../cancel` cancels an unfinished command
- `POST /api/v1/agent/enroll`, `.../identity`, `.../heartbeat`, `.../commands/claim`, `.../commands/{commandID}/start`, `.../commands/{commandID}/result` — management-agent endpoints, authenticated by per-agent Ed25519 request signatures (never browser sessions); every response is signed by Core

## Live traffic map (Windows native / ntopng)

**Windows capture and automatic setup:** see [Traffic sensor setup](docs/TRAFFIC_SENSOR_SETUP.md).
A fresh Windows installation enables a separate native ETW traffic sensor. Docker
Desktop does not capture the host NIC: `nerve-sensor.exe` runs on Windows and sends
real TCP/UDP endpoint, direction, byte and timing metadata to Core. No additional
Linux machine, ntopng installation or packet driver is required. The selected
physical default-route adapter appears in Map → Diagnostics.

Capture covers this host on the selected adapter's local addresses. The world map
shows external peers; LAN-wide monitoring still requires appropriate agents,
gateway capture, SPAN or TAP. Set your local site coordinates (manual or optional
browser location) once so arcs have an honest origin.

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

To verify ntopng directly, use its documented endpoints (curl prompts for the password):

```sh
curl --user nerve-reader 'https://ntopng.example.lan:3001/lua/rest/v2/get/ntopng/interfaces.lua'
curl --user nerve-reader 'https://ntopng.example.lan:3001/lua/rest/v2/get/flow/active.lua?ifid=0&perPage=10'
```

References: [ntopng REST v2 examples](https://www.ntop.org/guides/ntopng/api/rest/examples_v2.html)
and [active.lua implementation](https://github.com/ntop/ntopng/blob/dev/scripts/lua/rest/v2/get/flow/active.lua).
NERVE parses active.lua (not the different active_list.lua schema): client/server IP
and port, protocol.l4/l7, integer bytes, thpt.bps, and first/last_seen. thpt.bps is
bits per second and is divided by eight internally. The UI formats throughput as bit/s.

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

Map implementation and limits (World view and the shared snapshot; the 3D view is described in [docs/TRAFFIC_MAP_3D.md](docs/TRAFFIC_MAP_3D.md)):

- Natural Earth 1:50m countries are bundled in `public/maps`, rendered once with
  d3-geo Natural Earth projection and topojson-client. No CDN, tiles, street map,
  external geocoding, or invented polygons. Attribution/license: `public/maps/NOTICE.md`.
- Public remote IPs from both Windows and ntopng use locally bundled DB-IP City
  Lite (September 2026) city/region estimates. No IP lookup calls an external service.
  Country-only results use Natural Earth representatives verified inside the rendered
  country's land geometry. Unknown locations remain unplaced but inspectable.
  Locations are approximate, never verified physical server positions; arcs do not
  describe actual routing. See [geolocation details](docs/TRAFFIC_GEOLOCATION.md).
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

Manual acceptance test:

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

Validation commands:

```sh
go test ./...
go vet ./...
# Use a disposable PostgreSQL database; integration tests create isolated schemas.
# Set NERVE_TEST_DATABASE_URL before this command.
go test -tags=integration -count=1 ./...
npm ci
npm run typecheck
npm run test:map
npm run build
docker compose build api web
```

CI (`.github/workflows/ci.yml`) runs all of the above against PostgreSQL 17. It also builds the
web and API images, the Windows and Linux discovery-agent artifacts, and the Windows traffic sensor.

## Architecture and next work

See [IMPLEMENTATION_PLAN.md](./IMPLEMENTATION_PLAN.md) for the current state, architecture, data model, forward plan, risks and privilege boundary, and [docs/REMOTE_MANAGEMENT.md](docs/REMOTE_MANAGEMENT.md) for the remote-management proposal.
