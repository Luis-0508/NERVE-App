# NERVE implementation plan

This plan records what NERVE actually contains today, how the delivery order diverged
from the original sequence, and the forward plan. User-facing capabilities are described
in [README.md](./README.md); this file is for sequencing, architecture and risk.

## Current state (October 2026)

### Implemented

| Area | What exists | Where |
| --- | --- | --- |
| Foundation | Next.js static UI, Go API, PostgreSQL with checksummed ordered migrations, Compose + Nginx proxy, health/readiness, SSE with one shared PostgreSQL listener | `app/`, `cmd/nerve-server`, `internal/platform`, `deploy/` |
| Authentication | First-admin bootstrap, hashed opaque sessions, bounded login rate limiting, roles (`administrator`, `technician`, `read_only`), immutable `audit_log` (trigger-enforced) for auth events | `internal/auth`, `migrations/000001` |
| Inventory | Normalized devices/interfaces/addresses/services/relationships; server-side search, sort, filter, paging; device detail view | `internal/inventory`, `components/devices` |
| Discovery | One administrator-configured CIDR, persistent scan jobs, database-enforced single active scan, MAC-first reconciliation, consistent online/offline lifecycle | `internal/discovery` |
| Native discovery agent | Go agent for Windows/Linux: ICMP, ARP/NDP neighbour cache, bounded TCP fallback, parallel reverse DNS | `cmd/nerve-agent`, `Dockerfile.agent` |
| Windows traffic sensor | Native .NET ETW sensor as a Windows service; HMAC-signed, nonce/timestamp-protected reports to a loopback-only ingest listener | `native/windows-sensor`, `internal/traffic/sensor.go` |
| ntopng | Read-only REST v2 active-flow adapter; credentials stay server-side | `internal/integrations/ntopng` |
| Live traffic API | `GET /api/v1/traffic/live`: normalized, aggregated, bounded snapshot with explicit error codes and capture scope | `internal/traffic`, `internal/httpapi` |
| Geographic Traffic Map | Natural Earth map, offline DB-IP City Lite geolocation, honest local-site provenance | `components/map` |
| 3D Traffic Map | Inventory devices on derived /24 plates, measured flows routed through the uplink to remote endpoints on a 3D globe | `components/map/topology*` |

### Missing

- Device intelligence: human-owned names/aliases, tags, notes, trust/approval state,
  identity evidence, manual-vs-discovered provenance, merge/split workflows. The
  `devices` table has `owner`, `location`, `notes` and `approved_at` columns, but no API or
  UI writes them.
- Discovery 2.0: multiple targets, scheduled scans (`subnets.discovery_interval_seconds`
  exists but nothing schedules), incremental progress, idempotent report ingestion.
- Agent identity: every discovery agent shares one global `NERVE_DISCOVERY_AGENT_TOKEN`;
  the traffic sensor shares one `NERVE_SENSOR_TOKEN`. No enrollment, rotation or revocation.
- Real local topology: no LLDP/CDP/SNMP, switch-port, gateway or device-to-device
  relationships are collected. The `relationships` table is unused by any collector.
- Audit coverage: only bootstrap/login/logout write `audit_log`. Administrator discovery
  changes (target, scan requests) are not yet audited.
- Integrations: Proxmox and Docker (schema tables exist, no adapters).
- Change intelligence: no "what changed?" view beyond raw discovery events.
- Remote management: the `actions`/`action_runs` tables exist, but nothing creates or
  executes actions.
- Operations: no releases, tags, changelog, licence file, backup/restore, retention policy,
  diagnostics bundle or upgrade/rollback procedure.

## How delivery diverged from the original plan

The original sequence was Foundation → Inventory → Discovery → Reliability (3.5) →
**Device intelligence (4)** → Discovery 2.0 (4.5) → **Topology (5)** → Proxmox → Docker →
Change intelligence → Remote actions → Operations.

Milestones 1–3.5 shipped in that order. After 3.5 the next work became traffic
visualization instead of Milestone 4. Live traffic (Windows ETW sensor, ntopng, geographic
map) shipped as "Milestone 5: map" (PR #21), and the 3D view followed (PR #27). That 3D
view is **not** the planned Milestone 5 topology. It has no server-side graph
projection and no observed local links. It derives /24 plates from addresses, and it draws
only measured flows from local hosts to remote endpoints. The original plan warned not to
build topology before identity semantics were stable, and Milestone 4 is still open. The 3D
view stays honest by labelling all derived structure. A real topology still needs
Milestone 4 and real link collection.

## Forward plan

The order is set by dependencies. Remote management needs a trustworthy device identity
and a trustworthy agent identity, so both come first.

1. **Device identity & intelligence** (former Milestone 4). Human-owned name/alias,
   tags, notes, owner/location, trust/approval state; per-field provenance (manual vs
   discovered, so discovery never overwrites operator input); an identity-evidence view
   built on `device_identities`; merge/split with audit records; secondary-address
   matching in the Traffic Map. This is the anchor that agents, MeshCentral nodes,
   Proxmox VMs and containers all map to.
2. **Agent identity & enrollment.** Per-agent keys or certificates, one-time enrollment
   tokens, revocation, rotation, `discovery_agents` keyed by enrolled identity, and
   capability reporting. Retire the shared discovery token and the shared sensor token.
   This is a hard prerequisite for any remote execution (see
   [Remote management proposal](docs/REMOTE_MANAGEMENT.md)).
3. **Remote management, phase 1.** Read-only endpoint presence and deep links/handoff
   through the chosen execution layer. Every request is audited.
4. **Remote management, phase 2.** Confirmed, capability-checked, time-bounded actions
   with operator identity, output limits and immutable audit.
5. **Discovery 2.0.** Multiple targets, scheduled scans, idempotent incremental reports.
6. **Local topology.** LLDP/CDP/SNMP and switch-port awareness, gateway/ARP-derived
   relationships, all with provenance and confidence, then a server-side graph projection
   that the 3D view can render instead of derived /24 plates.
7. **Proxmox, then Docker.** Read-only inventory first. Lifecycle actions reuse the
   remote-management action pipeline. Docker access only through a protected socket proxy
   or a mutually authenticated remote API.
8. **Change intelligence.** Explainable unknown-device, identity, address, service and
   offline/return events.
9. **Operations & releases.** Versioned releases and changelog, licence, backup/restore,
   retention, diagnostics bundle, upgrade/rollback, SBOM/provenance. Start versioning and
   tagging as early as remote management ships agents, because agent upgrades need
   releases.

## Architecture

```text
app/, components/, lib/      Next.js static UI; typed same-origin /api/v1 client
cmd/nerve-server/            Go Core (API, SSE, migrations, traffic-check)
cmd/nerve-agent/             Native discovery agent (Windows/Linux)
native/windows-sensor/       .NET ETW traffic sensor (Windows service)
internal/auth|config|discovery|httpapi|inventory|traffic|platform/database
internal/integrations/<x>/   One package per external adapter (ntopng today)
migrations/                  Ordered, checksummed PostgreSQL migrations; never edit applied ones
deploy/                      Nginx reverse proxy
```

`entities` gives stable IDs, names, kinds and lifecycle status to everything graphable.
Typed tables hold devices, interfaces, IP addresses, subnets, physical ports,
services/endpoints, hypervisors/VMs and containers. `relationships` joins any two
entities with provenance, confidence, observation times and manual confirmation.
`observations`, `events`, `metrics` and `audit_log` are append-only histories.
`integrations`/`integration_credentials` and `actions`/`action_runs` keep adapters,
encrypted secrets and future control actions apart, so secrets never enter browser
responses or audit parameters. Domain packages are added with their first real behaviour,
never as empty scaffolding.

## Technical risks

- Identity reconciliation must tolerate changing IPs, duplicate hostnames, randomized
  MACs, multiple interfaces and contradictory sources, and show evidence rather than false
  certainty.
- Shared agent secrets are acceptable for read-only discovery on one host. They are not
  acceptable once agents can execute actions.
- Report ingestion must be idempotent before batched progress or remote command results
  are introduced.
- Dense topology needs server-side projection and level of detail. The 3D view currently
  caps inventory at 1,000 devices and flows at 500 groups.
- Integration credentials need authenticated encryption, key rotation, redaction and
  recovery guidance before the first adapter stores one.
- Realtime needs reconnect/resume semantics and event IDs before UI state depends on it.
- Docker socket access is root-equivalent and must never reach the web/API process.
- Observation, event, metric and audit retention must be defined before high-frequency
  telemetry.
- Dependencies: pgx is held at 5.10 until pgxmock supports pgx 5.11 (`.github/dependabot.yml`).

## Privileges and credentials

Raw ICMP, ARP/NDP, low-port probing, packet capture and flow listeners may need
`CAP_NET_RAW`, `CAP_NET_ADMIN` or host networking, so they run in native host agents, never in
the Core container. Windows ETW capture runs as a dedicated service. Its ingest
listener is published only on host loopback and never through Nginx.

Discovery needs only the agent token. Proxmox tokens, Docker TLS/SSH material, SNMP
communities, SSH/WinRM accounts, MeshCentral credentials and vendor API keys are
introduced only with their adapters, encrypted at rest, and never sent to the browser.
Observed MAC addresses are never sent to third parties for enrichment.
