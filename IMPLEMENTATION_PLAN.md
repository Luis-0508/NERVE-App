# NERVE implementation plan

This plan records what NERVE actually contains today, how the delivery order diverged
from the original sequence, and the forward plan. User-facing capabilities are described
in [README.md](./README.md); this file is for sequencing, architecture and risk.

## Current state (October 2026)

### Implemented

| Area | What exists | Where |
| --- | --- | --- |
| Foundation | Next.js static UI, Go API, PostgreSQL with checksummed ordered migrations, Compose + Nginx proxy, health/readiness, SSE with one shared PostgreSQL listener | `app/`, `cmd/nerve-server`, `internal/platform`, `deploy/` |
| Authentication & authorization | First-admin bootstrap, hashed opaque sessions, bounded login rate limiting, roles mapped to explicit permissions in `role_permissions`, immutable `audit_log` (trigger-enforced) for auth, discovery administration, device identity, agents and remote commands | `internal/auth`, `internal/authz`, `internal/audit`, `migrations/000001`, `000005` |
| Inventory | Normalized devices/interfaces/addresses/services/relationships; server-side search, sort, filter, paging; device detail view | `internal/inventory`, `components/devices` |
| Discovery | One administrator-configured CIDR, persistent scan jobs, database-enforced single active scan, MAC-first reconciliation, consistent online/offline lifecycle | `internal/discovery` |
| Native discovery agent | Go agent for Windows/Linux: ICMP, ARP/NDP neighbour cache, bounded TCP fallback, parallel reverse DNS | `cmd/nerve-agent`, `Dockerfile.agent` |
| Windows traffic sensor | Native .NET ETW sensor as a Windows service; HMAC-signed, nonce/timestamp-protected reports to a loopback-only ingest listener | `native/windows-sensor`, `internal/traffic/sensor.go` |
| ntopng | Read-only REST v2 active-flow adapter; credentials stay server-side | `internal/integrations/ntopng` |
| Live traffic API | `GET /api/v1/traffic/live`: normalized, aggregated, bounded snapshot with explicit error codes and capture scope | `internal/traffic`, `internal/httpapi` |
| Geographic Traffic Map | Natural Earth map, offline DB-IP City Lite geolocation, honest local-site provenance | `components/map` |
| 3D Traffic Map | Inventory devices on derived /24 plates, measured flows routed through the uplink to remote endpoints on a 3D globe | `components/map/topology*` |
| Agent identity | Single-use, expiring enrollment tokens (digest only); per-agent Ed25519 keys (public key only in PostgreSQL); signed requests with nonce replay protection; revoke, re-enroll, remove; Core signs every agent-API response with a key pinned at enrollment | `internal/agents`, `internal/agentproto`, `migrations/000005`, `000006` |
| Remote management foundation | Heartbeat and capabilities, evidence-based device association, provider-neutral `remote.Provider` registry, audited at-most-once command pipeline with five read-only commands, minimal device and settings UI | `internal/remote`, `cmd/nerve-agent`, `components/remote`, `migrations/000007` |
| Agent service & lifecycle | `nerve-agent service install/uninstall/start/stop/status`, Windows service (virtual account, recovery actions, event log) and hardened systemd unit (unprivileged user, watchdog), PowerShell/shell installers, `config.json`, private-CA TLS, `diagnose`, run mode and graceful-stop reporting, live re-enrollment | `cmd/nerve-agent/service*.go`, `scripts/agent`, `migrations/000010` |
| Device identity | Human-owned name, type, tags, owner, location, notes and trust state (trigger-protected from discovery); sourced facts with confidence; identity evidence with strength; explicit discovery match rules; agent facts as device evidence; duplicate candidates; transactional merge and split with tombstones and append-only records | `internal/devices`, `internal/discovery/match.go`, `migrations/000008`, `000009`, `components/devices` |

### Missing

- Device identity follow-ups: vendor (OUI) lookup and automatic classification; more
  evidence sources (DHCP, SNMP, LLDP, ARP history); `devices.approved_at` is superseded by
  trust state and unused.
- Discovery 2.0: multiple targets, scheduled scans (`subnets.discovery_interval_seconds`
  exists but nothing schedules), incremental progress, idempotent report ingestion.
- Shared local secrets: the discovery worker still uses the shared `NERVE_DISCOVERY_AGENT_TOKEN`
  and the traffic sensor the shared `NERVE_SENSOR_TOKEN`. Both should move to enrolled agent
  identities. Management agents already use per-agent keys.
- Real local topology: no LLDP/CDP/SNMP, switch-port, gateway or device-to-device
  relationships are collected. The `relationships` table is unused by any collector.
- Integrations: Proxmox and Docker (schema tables exist, no adapters).
- Change intelligence: no "what changed?" view beyond raw discovery events.
- Remote management beyond the foundation and agent services: no privileged commands,
  interactive sessions, file transfer, MeshCentral adapter, MSI package or signed agent
  updates. The
  unused `actions`/`action_runs` tables from migration 000001 are superseded by
  `remote_commands` and can be dropped in a later migration.
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
build topology before identity semantics were stable. Milestone 4 (device identity) shipped
in October 2026; the 3D view stays honest by labelling all derived structure. A real
topology still needs real link collection.

## Forward plan

The order is set by dependencies. Remote management needs a trustworthy device identity
and a trustworthy agent identity. Both shipped in October 2026: agent identity with the read-only
remote-management foundation, then device identity.

1. **Device identity — implemented (October 2026)** (former Milestone 4). Human-owned
   metadata and trust state, provenance, identity evidence, duplicate candidates, merge and
   split; agents and discovery converge on one record. See
   [Device identity](docs/DEVICE_IDENTITY.md). Still open: OUI/vendor and classification,
   more evidence sources, multi-address matching in the Traffic Map.
2. **Remote management foundation — implemented (October 2026).** Agent enrollment and
   identity, Core-signed responses, heartbeat, capabilities, device association, audited
   read-only command pipeline. See [Remote management](docs/REMOTE_MANAGEMENT.md).
   Still open from this area: move the discovery worker and traffic sensor onto enrolled
   identities; agent-initiated key rotation; Core key rotation without re-enrollment.
3. **Production RMM milestone (in progress, October 2026).** Delivered as small PRs in
   dependency order. Each builds on `internal/remote`, the signed agent protocol, the
   command pipeline and device trust; none adds a second backend or execution path.

   | PR | Scope | State |
   | --- | --- | --- |
   | 1 | Agent as Windows service / systemd unit with unprivileged accounts, installers, persistent config, private-CA TLS, diagnostics, run state and graceful-stop reporting (`connection`: online/stopped/offline), live re-enrollment | implemented |
   | 2 | Security hardening and privileged execution architecture: HTTPS enforcement for privileged agents, per-command operator approval the agent can verify, device-level scopes, trusted-device policy check, separate privileged helper (Windows service as LocalSystem / root systemd unit) reachable only over a local authenticated channel, output redaction and transcript retention | planned |
   | 3 | System-management commands on that foundation: service restart/stop, process termination, reboot/shutdown, log retrieval, detailed hardware/network inventory, each with permission, validation, confirmation and audit | planned |
   | 4 | RMM device page: overview, system, performance, processes, services, software, actions, command history, agent status, audit; loading/offline/denied/failed states | planned |
   | 5 | Bounded non-interactive remote commands (PowerShell, shell) with timeouts, cancellation, polled output and protected transcripts | planned |
   | 6 | File management in explicitly authorized roots: browse, upload, download, size limits, traversal protection, integrity checks | planned |
   | 7 | Versioned, signed agent releases and authorized remote updates with verification and rollback | planned |
   | 8 | Documentation and end-to-end verification on real Windows and Linux hosts | planned |

   Privileged commands stay blocked until PR 2 lands. Blocked until all of these exist: HTTPS
   enforced for agents that may run privileged capabilities (Core-signed responses give
   authenticity, not confidentiality); the target device in the operator-owned
   `trusted` state (the trust model exists; the policy check comes with the first privileged
   command); explicit operator confirmation showing target
   identity and evidence; per-device/group authorization scope and command policies;
   a privileged helper separate from the unprivileged agent; signed agent releases and
   updates; transcript retention and redaction rules for stdout/stderr.
4. **Interactive sessions.** A MeshCentral adapter behind `remote.Provider` (time-boxed
   spike first: read-only node listing, operator-confirmed `meshcentral` identities, deep
   links before embedding, audited handoff).
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
cmd/nerve-agent/             Native agent (Windows/Linux): discovery worker and enrolled management agent
native/windows-sensor/       .NET ETW traffic sensor (Windows service)
internal/auth|authz|audit|config|discovery|httpapi|inventory|traffic|platform/database
internal/agents|agentproto   Agent enrollment, identity, signed protocol, association
internal/remote              Provider-neutral remote management: providers, policies, commands
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
  MACs, multiple interfaces and contradictory sources. Device identity now records the
  evidence and match rule for every observation and surfaces conflicts as duplicate
  candidates instead of merging. Remaining gap: devices seen only by IP (no MAC, no agent)
  can still be split across records when addresses change.
- Shared agent secrets remain only for local discovery and the traffic sensor. Management
  agents use per-agent keys; privileged actions additionally need HTTPS (see the forward plan).
- Discovery report ingestion is still not idempotent; remote command results are.
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

Discovery needs only the shared local token. Management agents hold only their own Ed25519
key; Core holds `NERVE_CORE_SIGNING_KEY` (environment only, never stored in PostgreSQL). Proxmox tokens, Docker TLS/SSH material, SNMP
communities, SSH/WinRM accounts, MeshCentral credentials and vendor API keys are
introduced only with their adapters, encrypted at rest, and never sent to the browser.
Observed MAC addresses are never sent to third parties for enrichment.
