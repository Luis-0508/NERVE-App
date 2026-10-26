# HTTP API

Endpoints served by the Go Core. Browser endpoints use same-origin session cookies through
the Nginx proxy; agent and sensor endpoints use their own credentials and never accept
browser sessions. Permissions in parentheses are checked server-side.

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
- `GET /api/v1/devices` — authenticated device inventory with effective and discovered names and types, trust state and tags; accepts `search`, `status`, `classification`, `trust`, `tag`, `sort`, `direction`, `limit`, and `offset` (at most 100,000)
- `GET /api/v1/devices/facets` — status, type, trust and tag values present in the inventory
- `GET /api/v1/devices/vocabulary` — allowed trust states and type overrides
- `POST /api/v1/devices/{deviceID}/metadata` — set or clear the manual name, type, tags, owner, location and notes (`devices.manage`, audited)
- `POST /api/v1/devices/{deviceID}/trust` — change the trust state with an optional reason (`devices.trust`, audited)
- `GET /api/v1/devices/{deviceID}/identity` — identity evidence with strength, conflicts, agents, sourced facts and discovery match history
- `GET /api/v1/devices/duplicates` — likely duplicate pairs with reasons (`?deviceId=` for one device); `POST /api/v1/devices/duplicates/dismiss` marks a pair as different devices (`devices.merge`)
- `POST /api/v1/devices/{deviceID}/merge` — merge `sourceDeviceId` into this device (`devices.merge`, audited)
- `POST /api/v1/devices/{deviceID}/split` — move chosen interfaces and identities into a new device, with an explicit agent decision (`devices.merge`, audited)
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
- `POST /api/v1/agent/enroll`, `.../identity`, `.../credential/rotate`, `.../heartbeat`, `.../commands/claim`, `.../commands/{commandID}/start`, `.../commands/{commandID}/result` — management-agent endpoints, authenticated by per-agent Ed25519 request signatures (never browser sessions). Every response is signed by Core, except the `503 core_signing_key_missing` answer returned when Core has no signing key; agents reject that unsigned answer and log it as an unverifiable HTTP 503
