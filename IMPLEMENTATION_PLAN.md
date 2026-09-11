# NERVE implementation plan

## Repository state

The repository began as an empty Git worktree. Milestones 1 through 3 now provide the working foundation, database-backed device inventory, and local network discovery. Milestone 3.5 hardens those slices before new product surface is added.

## Target structure

```text
app/                         Next.js routes and shared design tokens
components/                  Product-specific React UI
lib/                         Typed frontend API boundary
cmd/nerve-server/            Go control-plane entrypoint
cmd/nerve-agent/             Native host discovery worker
internal/
  auth/                      Password and session handling
  config/                    Environment configuration
  discovery/                 Scan jobs, reconciliation, persistence
  httpapi/                   Versioned HTTP API, middleware, realtime fan-out
  platform/database/         PostgreSQL connection and migration runner
migrations/                  Ordered PostgreSQL migrations
deploy/                      Reverse-proxy configuration
```

Integrations will live under `internal/integrations/<adapter>`. Domain packages are introduced with their first real behavior instead of being generated empty.

## Data model

`entities` provides stable IDs, names, kinds, lifecycle status, and adapter metadata for graphable objects. Typed tables hold normalized fields for devices, interfaces, IP addresses, subnets, physical ports, services/endpoints, hypervisors/VMs, and container resources. `relationships` joins any two entities and records provenance, confidence, observation times, and manual confirmation.

Immutable histories are kept in `observations`, `events`, `metrics`, and `audit_log`. Integrations, encrypted credentials, declared actions, and action runs are separated so secrets never enter browser responses or audit parameters. Authentication uses users, roles, user-role assignments, and hashed opaque sessions.

## Milestone sequence

1. **Foundation** — web shell, themes, Go API, PostgreSQL, migrations, authentication, SSE, health/readiness, Compose, documentation.
2. **Entity model / inventory** — repository/query layer plus device, interface, address, service, relationship, event, and observation APIs; real sortable/filterable device inventory and detail views.
3. **Local discovery** — subnet configuration, persistent scan jobs, host agent, reachability evidence, identity reconciliation, and discovered-device UI.
3.5. **Reliability & quality gate** — database-enforced scan concurrency, consistent child lifecycle state, bounded login-rate state, shared realtime listener, faster reverse DNS, stronger CI production builds, dependency-update grouping, and current documentation.
4. **Device intelligence** — edit/override device names and metadata, notes, tags, classification, approval/trust state, identity evidence, merge/split workflows, and explicit manual-vs-discovered provenance.
4.5. **Discovery 2.0** — multiple discovery targets, scheduled scans, incremental progress/report batches, configurable probing, stronger agent identity, and durable service installation where appropriate.
5. **Topology** — normalized graph projection API, XYFlow rendering, inspector, progressive disclosure, saved layouts, filters, provenance/confidence views, and realtime updates.
6. **Proxmox** — credential configuration, read-only node/VM/LXC/network inventory first; audited start/stop/restart actions only after capability and permission checks are proven.
7. **Docker** — host/container/network/port inventory first; audited lifecycle actions and logs after a protected socket-proxy or mutually authenticated remote API boundary exists.
8. **Change intelligence / security** — explainable unknown-device, identity-change, address-change, service/port-change, offline/return events, and a first-class “what changed?” view.
9. **Remote actions** — capability-driven, confirmed, idempotent where possible, timeout-bounded actions for VMs/containers and later managed endpoints.
10. **Operations & releases** — backup/restore, retention, diagnostics bundle, upgrade/rollback guidance, release versioning, SBOM/provenance, and production deployment guidance.

## Completed slices

Milestone 2 delivered the read-only device inventory slice: normalized repositories and APIs, database-backed facets and queries, a sortable/filterable inventory table, and device detail sections for network identity, services, events, relationships, and notes.

Milestone 3 delivered the first functional local-discovery slice. Administrators configure a bounded CIDR and queue a persistent scan from Devices. A token-authenticated native host agent claims work, performs bounded concurrent ICMP/TCP reachability checks, reads the host ARP/NDP cache, resolves reverse DNS, and reports per-host evidence without aborting on isolated failures. The Core reconciles primarily by MAC and secondarily by the subnet/IP binding, persists normalized devices/interfaces/addresses/observations/events, preserves first-seen timestamps, advances last-seen timestamps, and marks previously bound devices offline when they are absent from a successfully completed follow-up scan.

Milestone 3.5 closes reliability gaps exposed by that first live slice. PostgreSQL now owns the single-active-scan invariant so concurrent callers cannot create duplicate active jobs. Local-discovery device, interface, and IP entities transition together between online and offline states. Guessed remote interface media types are removed rather than presenting invented facts. Reverse DNS is bounded and parallel. Realtime event delivery uses one PostgreSQL listener with bounded per-client buffers instead of holding a database connection per browser tab. Login rate-limit entries are pruned after their window expires. CI verifies Go tests/vet, PostgreSQL integration flows, frontend type/build checks, the actual web production image, the API image, and both Windows and Linux discovery-agent artifacts.

Windows uses a native agent because Docker Desktop's Linux VM and layer-4 host networking cannot provide the host's real layer-2 neighbor view. Linux server/VM deployments use the same agent binary directly in the target host namespace. The Core container receives no raw-socket, host-network, or Docker-socket privileges.

## Technical risks

- Identity reconciliation must tolerate changing IPs, duplicate hostnames, randomized MACs, multiple interfaces, and contradictory sources. Milestone 4 must expose evidence instead of silently pretending certainty.
- Network discovery is platform-specific and must run with narrowly scoped privileges.
- Incremental scan progress must not double-count report retries; report ingestion should become idempotent before batched progress is introduced.
- Dense topology needs server-side projection and frontend level-of-detail to avoid graph-wide rerenders.
- Integration credentials require authenticated encryption, key rotation, redaction, and recovery guidance.
- Realtime delivery now has bounded fan-out, but reconnect/resume semantics and event IDs are still required before realtime becomes critical UI state.
- Control actions must enforce server-side capabilities, idempotency where possible, confirmation, timeouts, and immutable auditing.
- Docker socket access is effectively root-equivalent and must not be granted directly to the web/API process.
- Observation, event, metric, and audit retention must be defined before high-frequency discovery or telemetry is enabled.

## Privileges and credentials

Raw ICMP, ARP/NDP, low-port probing, passive packet capture, and NetFlow/sFlow listeners may require `CAP_NET_RAW`, `CAP_NET_ADMIN`, or privileged host networking depending on the deployment. Docker discovery/actions require a protected socket proxy or mutually authenticated remote API. Future switch changes and endpoint management require adapter-specific privileges.

Local discovery requires only the randomly generated agent token used to authenticate host-to-Core reports; it needs no external network credentials. Proxmox API tokens, Docker TLS/SSH material, SNMP communities, SSH/WinRM accounts, network-vendor API credentials, and GeoIP licensing are introduced only with their adapters.

## Next concrete tasks

After Milestone 3.5 is manually smoke-tested and merged, start **Milestone 4 — Device intelligence**. The first vertical slice should let an administrator inspect why NERVE believes a device identity, approve/classify it, add human-owned metadata without discovery overwriting it, and safely merge duplicate identities. Do not start topology until those identity and provenance semantics are stable enough that the graph is visualizing trusted state rather than raw discovery guesses.

Vendor/OUI enrichment may be added from a versioned local database; observed MAC addresses must never be sent to a third party merely for enrichment.
