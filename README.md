<div align="center">

<img src="public/mark.svg" alt="" width="88" height="88">

# NERVE

**Self-hosted network inventory, discovery and live traffic visualization that shows what is observed and what is derived.**

[![Next.js](https://img.shields.io/badge/Next.js-16-000000?logo=nextdotjs&logoColor=white)](https://nextjs.org/)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![TypeScript](https://img.shields.io/badge/TypeScript-7-3178c6?logo=typescript&logoColor=white)](https://www.typescriptlang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org/)

<!-- Add one strong real hero screenshot of the current UI (for example the Overview or
Traffic Map) under docs/images/ once one exists. Do not use demo or fabricated data.

<img src="docs/images/hero.webp" alt="NERVE overview" width="900">
-->

</div>

## Overview

NERVE is a self-hosted platform for administrators who run their own networks. It keeps an inventory of devices, services and
their relationships, discovers hosts on a local subnet, shows live traffic on a world map and
a 3D topology view, and is growing into remote management through enrolled host agents.

Its focus is honest provenance. Every device explains why NERVE believes it is one device,
discovered values stay separate from what operators set, and the traffic views mark
approximate geography and missing data instead of inventing it. No screen uses static or
demo data.

Technically, a Go Core owns PostgreSQL and serves a JSON API, Server-Sent Events and health
endpoints; a static Next.js UI talks to it same-origin through Nginx; and everything that
needs raw network access (discovery, traffic capture, remote management) runs as a separate
native agent or sensor outside the Core container.

## Features

- **Sign-in, roles and permissions**: first-administrator setup, hashed server-side sessions, rate-limited login. Roles (`administrator`, `technician`, `read_only`) map to explicit server-side permissions stored in the database. An immutable audit log records authentication, discovery administration, device identity changes, agent lifecycle and remote commands.
- **Device inventory**: PostgreSQL-backed devices, interfaces, addresses, services and relationships, with server-side search, sort, filter and paging, and a device detail view. Search covers manual and discovered names, every current and past IP, every MAC and tags.
- **Device identity**:
  - Operators own a device's name, type, tags, owner, location, notes and trust state (`unknown`, `reviewed`, `trusted`, `blocked`). Discovery keeps updating discovered values but cannot overwrite these; PostgreSQL enforces it.
  - Each device explains why NERVE believes it is one device: identity evidence with its strength, and discovered facts with their source.
  - Likely duplicate records are listed for review.
  - Administrators can merge and split records; history is preserved and every step is audited.
  - See [Device identity](docs/DEVICE_IDENTITY.md).
- **Local discovery**: an administrator configures one subnet and starts scans from **Devices**. A native host agent (Windows or Linux) collects ICMP/TCP reachability, ARP/NDP neighbours and reverse DNS. Core reconciles devices by MAC first, then subnet/IP, and tracks online/offline state. See [Local discovery](docs/LOCAL_DEPLOYMENT.md#local-network-discovery).
- **Live traffic**: a native Windows ETW traffic sensor (host traffic only) or an external ntopng source, exposed as a normalized, bounded snapshot with explicit error states. See [Traffic sensor setup](docs/TRAFFIC_SENSOR_SETUP.md).
- **Traffic Map**: a geographic **World** view, and a **Topology 3D** view that places inventory devices on derived /24 plates and routes measured flows through the local uplink to remote endpoints on a globe. See [Traffic map](docs/TRAFFIC_MAP.md) and [3D view](docs/TRAFFIC_MAP_3D.md).
- **Remote management (foundation)**: per-host NERVE agents install as a Windows service or systemd unit under an unprivileged account, enroll with a single-use token and get their own Ed25519 identity, report heartbeats, run state (online, stopped, offline) and capabilities, and are linked to inventory devices from evidence (operator-confirmable). Operators with the right permission can run five read-only commands (system info, metrics, processes, services, installed software) from the device page; every step is audited. Core signs its responses so agents can reject forged commands. Interactive sessions and privileged commands are not available. See [Remote management](docs/REMOTE_MANAGEMENT.md) and [Agent setup](docs/AGENT_SETUP.md).

## Screenshots

<!-- Add 2–4 real screenshots (Overview, Devices, device identity, Traffic Map) under
docs/images/ as a two-column gallery once they exist. Use real, non-sensitive data only. -->

No screenshots yet.

## Quick start

### Windows

NERVE must be started as the complete Compose stack; `npm run dev` starts only the frontend and intentionally does not provide an `/api/v1` proxy.

1. Install and start Docker Desktop with the Compose v2 plugin.
2. Double-click `setup.bat`. No input is needed: it creates `.env` with cryptographically random database, discovery-agent and traffic-sensor secrets and the Core signing key for management agents, selects port 3000 (or a free port if occupied), and generates the ignored local `start.bat` and `stop.bat`. Existing configuration, ports, secrets and custom launchers are retained; official historical stock launchers are upgraded automatically. Launcher migration details and maintainer instructions: [Windows traffic sensor](docs/WINDOWS_TRAFFIC_SENSOR.md#launcher-upgrades). Successful setup closes automatically after a short completion message.
3. Run `start.bat` from Explorer, PowerShell, or Command Prompt. Both scripts always change to their own project directory first and keep the console open until a key is pressed.
4. The start script builds/starts Core, the Windows discovery agent and the native Windows traffic sensor. Accept the first sensor service installation UAC prompt; subsequent unchanged starts need no elevation. Sensor failures are reported separately and never stop Core/discovery. The browser opens after Core/discovery readiness.

Secrets, HTTPS and LAN access, stopping, runtime checks and discovery details are in
[Running NERVE locally](docs/LOCAL_DEPLOYMENT.md).

### Docker Compose

Docker is the intended local and production development path.

1. Copy `.env.example` to `.env`.
2. Replace `POSTGRES_PASSWORD` with a strong local secret.
3. Set `NERVE_COOKIE_SECURE=true` when the public endpoint uses HTTPS.
4. Run `docker compose up --build`.
5. Open `http://localhost:3000/setup` and create the first administrator.

The setup endpoint closes itself after the first user is created. Subsequent setup requests return HTTP 409.

## Development

### Frontend

Install Node.js 26 or use the production Docker build, then run:

```text
npm install
npm run dev
```

For a complete local session, expose the API through the same origin or use the included Compose proxy. The browser client intentionally uses same-origin `/api/v1` requests so privileged credentials and sessions are not distributed across origins in production.

### API

Install Go 1.26 or newer and PostgreSQL 17, configure the variables shown in `.env.example`, then run:

```text
go mod download
go test ./...
go run ./cmd/nerve-server
```

The service applies ordered `.up.sql` migrations at startup while holding a PostgreSQL advisory lock. It rejects a migration whose checksum changes after application.

### Checks

```sh
go test ./...
go vet ./...
# Use a disposable PostgreSQL database; integration tests create isolated schemas.
# Set NERVE_TEST_DATABASE_URL before this command.
go test -tags=integration -count=1 -p 1 ./...
npm ci
npm run typecheck
npm run test:map
npm run build
docker compose build api web
```

CI (`.github/workflows/ci.yml`) runs all of the above against PostgreSQL 17. It also builds the
web and API images, the Windows and Linux discovery-agent artifacts, and the Windows traffic sensor.

The HTTP API is listed in [docs/API.md](docs/API.md).

## Architecture

```text
app/, components/, lib/   Next.js UI (static export, same-origin /api/v1)
cmd/nerve-server/         Go Core: API, SSE, health, migrations
cmd/nerve-agent/          Native agent: discovery and remote management
internal/                 Core packages (auth, inventory, discovery, traffic, agents, ...)
native/windows-sensor/    Windows ETW traffic sensor (.NET)
migrations/               Ordered, checksummed PostgreSQL migrations
deploy/                   Nginx proxy configuration
```

Docker Compose runs PostgreSQL, the API and the web proxy. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for boundaries and data flow,
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) for the data model, privilege boundary and
forward plan, and [DESIGN.md](DESIGN.md) for the UI design system.

## Project status

NERVE is in active development and has no releases yet. Everything listed under Features
works today; remote management is a foundation with read-only commands only.

Not available yet: vendor (OUI) lookup and automatic device classification; multiple or scheduled discovery targets; observed local topology (LLDP/CDP/SNMP, switch ports, device-to-device links); Proxmox and Docker integrations; remote desktop, terminal, file transfer, shell or PowerShell commands and other privileged remote actions; releases and backup tooling. No screen uses static or demo data. The plan for these is in [IMPLEMENTATION_PLAN.md](./IMPLEMENTATION_PLAN.md), and the remote-management direction is in [docs/REMOTE_MANAGEMENT.md](docs/REMOTE_MANAGEMENT.md).

Current priorities and known limitations are tracked in
[docs/PROJECT_STATE.md](docs/PROJECT_STATE.md).

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

Issues, pull requests, commit messages, documentation, filenames, and repository-facing technical text are written in English so contributors can collaborate consistently.

## Security

Please do not report security-sensitive issues publicly. See [SECURITY.md](SECURITY.md).

## License

No license has been chosen yet. Until a license file is added, all rights are reserved.
