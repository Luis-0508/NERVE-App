# NERVE

NERVE is a self-hosted network management, infrastructure visualization, and remote management platform. The network is the product's primary navigation model: physical devices, virtual machines, containers, services, ports, and their relationships are represented in one provenance-aware entity graph.

This repository currently contains the Milestone 1 foundation and the Milestone 2 device inventory slice:

- a restrained, responsive Next.js application shell with individually tuned Light, Dark, and System themes;
- real login and first-administrator setup flows against the API;
- a Go control plane with versioned endpoints, structured logs, security headers, authentication rate limiting, health/readiness checks, and SSE;
- PostgreSQL migrations for authentication, graph entities, observations, events, integrations, actions, and immutable audit records;
- Docker Compose deployment with an Nginx reverse proxy; and
- isolated typed frontend API access with explicit connection, authentication, loading, and empty states;
- PostgreSQL-backed repositories and authenticated read APIs for devices, interfaces, IP addresses, service endpoints, and relationships; and
- a live device inventory with server-side search, sorting, filtering, pagination, and a device detail view.

No inventory screen uses static device data. Discovery, topology, Proxmox, and Docker remain deliberately unavailable until their own backend slices are implemented.

## Run with Docker Compose

Docker is the intended local and production development path.

1. Copy `.env.example` to `.env`.
2. Replace `POSTGRES_PASSWORD` with a strong local secret.
3. Set `NERVE_COOKIE_SECURE=true` when the public endpoint uses HTTPS.
4. Run `docker compose up --build`.
5. Open `http://localhost:3000/setup` and create the first administrator.

The setup endpoint closes itself after the first user is created. Subsequent setup requests return HTTP 409.

## Local frontend development

Install Node.js 22 or newer, then run:

```text
npm install
npm run dev
```

For a complete local session, expose the API through the same origin or use the included Compose proxy. The browser client intentionally uses same-origin `/api/v1` requests so privileged credentials and sessions are not distributed across origins in production.

## Local API development

Install Go 1.24 and PostgreSQL 17, configure the variables shown in `.env.example`, then run:

```text
go mod download
go test ./...
go run ./cmd/nerve-server
```

The service applies ordered `.up.sql` migrations at startup while holding a PostgreSQL advisory lock. It rejects a migration whose checksum changes after application.

## Endpoints

- `GET /health/live` — process liveness
- `GET /health/ready` — PostgreSQL readiness
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
- `GET /api/v1/stream` — authenticated server-sent event stream

## Architecture and next work

See [IMPLEMENTATION_PLAN.md](./IMPLEMENTATION_PLAN.md) for the target structure, normalized database model, milestone order, privilege boundary, integration credential requirements, risks, and next concrete development tasks.
