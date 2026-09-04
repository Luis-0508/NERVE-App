# NERVE implementation plan

## Repository state

The repository began as an empty Git worktree. There were no source files, documentation, migrations, reusable components, or existing architecture to preserve.

## Target structure

```text
app/                         Next.js routes and shared design tokens
components/                  Product-specific React UI
lib/                         Typed frontend API boundary
cmd/nerve-server/            Go control-plane entrypoint
internal/
  auth/                      Password and session handling
  config/                    Environment configuration
  httpapi/                   Versioned HTTP API, middleware, SSE
  platform/database/         PostgreSQL connection and migration runner
migrations/                  Ordered PostgreSQL migrations
deploy/                      Reverse-proxy configuration
.openai/                     Static preview hosting configuration
```

Integrations will later live under `internal/integrations/<adapter>` and discovery under `internal/discovery`. Domain packages will be introduced with their first real behavior instead of being generated empty.

## Data model

`entities` provides stable IDs, names, kinds, lifecycle status, and adapter metadata for graphable objects. Typed tables hold normalized fields for devices, interfaces, IP addresses, subnets, physical ports, services/endpoints, hypervisors/VMs, and container resources. `relationships` joins any two entities and records provenance, confidence, observation times, and manual confirmation.

Immutable histories are kept in `observations`, `events`, `metrics`, and `audit_log`. Integrations, encrypted credentials, declared actions, and action runs are separated so secrets never enter browser responses or audit parameters. Authentication uses users, roles, user-role assignments, and hashed opaque sessions.

## MVP sequence

1. Foundation: web shell, themes, Go API, PostgreSQL, migrations, authentication, SSE, health/readiness, Compose, documentation.
2. Entity model: repository/query layer plus device, interface, address, service, relationship, event, and observation APIs.
3. Discovery: subnet configuration, job execution, evidence collection, identity reconciliation, and discovered-device UI.
4. Devices: sortable/filterable table, detail sections, notes, classification, ports, relationships, and events.
5. Topology: one normalized graph projection, XYFlow rendering, inspector, progressive disclosure, and saved layouts.
6. Proxmox: credential configuration, inventory synchronization, capabilities, audited VM actions.
7. Docker: host/container/network synchronization, capabilities, audited lifecycle actions, logs.
8. Security/events: explainable unknown-device, identity-change, and port-change observations.

Milestone 2 now delivers the read-only device inventory slice across items 2 and 4: normalized repositories and APIs, database-backed facets and queries, a sortable/filterable inventory table, and device detail sections for network identity, services, events, relationships, and notes. Mutating inventory workflows remain intentionally deferred until discovery and authorization policies are defined.

## Technical risks

- Identity reconciliation must tolerate changing IPs, duplicate hostnames, randomized MACs, and contradictory sources.
- Network discovery is platform-specific and must run with narrowly scoped privileges.
- Dense topology needs server-side projection and frontend level-of-detail to avoid graph-wide rerenders.
- Integration credentials require authenticated encryption, key rotation, redaction, and recovery guidance.
- Realtime delivery needs bounded buffers and reconnect/resume behavior rather than global polling.
- Control actions must enforce server-side capabilities, idempotency where possible, confirmation, timeouts, and immutable auditing.
- Docker socket access is effectively root-equivalent and should not be granted to the web/API process casually.

## Privileges and credentials

Raw ICMP, ARP/NDP, low-port probing, passive packet capture, and NetFlow/sFlow listeners may require `CAP_NET_RAW`, `CAP_NET_ADMIN`, or privileged host networking depending on the deployment. Docker discovery/actions require a protected socket proxy or mutually authenticated remote API. Future switch changes and endpoint management require adapter-specific privileges.

Credentials are not required for the foundation or unauthenticated local discovery. Proxmox API tokens, Docker TLS/SSH material, SNMP communities, SSH/WinRM accounts, network-vendor API credentials, GeoIP licensing, and future agent enrollment secrets are introduced only with their adapters.

## First concrete tasks

- Complete and verify the foundation build.
- Run the initial migration against PostgreSQL and bootstrap the first administrator.
- Add authenticated entity/event repositories and API tests.
- Implement subnet configuration and a capability-scoped discovery job.
- Render discovered devices through the typed API; do not embed development arrays in components.
