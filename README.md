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

## Windows quick start

NERVE must be started as the complete Compose stack; `npm run dev` starts only the frontend and intentionally does not provide an `/api/v1` proxy.

1. Install and start Docker Desktop with the Compose v2 plugin.
2. Run `setup.bat`. The assistant asks for the local web port, creates `.env` with a cryptographically random database password, and generates the ignored local `start.bat` from `start.example.bat`. Existing local files are replaced only after confirmation.
3. Run `start.bat` from Explorer, PowerShell, or Command Prompt. Both scripts always change to their own project directory first and keep the console open until a key is pressed.
4. The start script runs `docker compose up -d --build`, waits for the web proxy, API, and PostgreSQL readiness, and opens the Windows default browser only after every check passes.

The generated database password is stored only in the ignored local `.env`; neither setup script contains a password. Manual setup remains possible by copying `.env.example` to `.env`, replacing `POSTGRES_PASSWORD=change-me`, and copying `start.example.bat` to `start.bat`.

Use `http://localhost:3000` unless `NERVE_WEB_PORT` was changed in `.env`. When a reliable active IPv4 address is available, the script also prints a LAN URL such as `http://192.168.1.25:3000`. Docker publishes the web proxy on all host interfaces. Windows Firewall and the local network must permit inbound access to the configured port.

The API remains reachable only through the web reverse proxy. For LAN access, Nginx forwards the original host and scheme and the API accepts only an `Origin` that matches that trusted proxy origin or an explicitly configured `NERVE_ALLOWED_ORIGIN`; origin checks are not disabled. Keep `NERVE_COOKIE_SECURE=false` for plain local HTTP and set it to `true` when NERVE is served through HTTPS.

To stop NERVE while preserving PostgreSQL data:

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
- `/api/v1/status` reports both overall `status: ok` and `database.status: ok`.

`docker compose ps` shows the container state. Use `docker compose logs --tail 100 postgres api web` when a service is not ready. The start script prints both automatically after a failed startup and does not open a browser in that case.

If a browser tab remains blank after rebuilding an older checkout, reload it without cache (`Ctrl+F5`). The production build includes `npm run test:runtime`, which verifies that the exported application shell, referenced static assets, and Nginx content-security policy are compatible with the Next.js bootstrap runtime.

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
- `GET /api/v1/stream` — authenticated server-sent event stream

## Architecture and next work

See [IMPLEMENTATION_PLAN.md](./IMPLEMENTATION_PLAN.md) for the target structure, normalized database model, milestone order, privilege boundary, integration credential requirements, risks, and next concrete development tasks.
