# Project state

Narrative snapshot of where NERVE stands. **The repository is authoritative:** if git, the
code, the task board or GitHub disagree with this file, trust them and correct this file.

- Last updated: 2026-10-26
- Verified against: `master` at `e0abdff`

## What exists

- Go Core with PostgreSQL (checksummed migrations up to `000013`), static Next.js UI, Nginx
  proxy, Docker Compose; health/readiness endpoints and SSE.
- Sign-in with first-admin bootstrap, roles mapped to permissions, immutable audit log.
- Device inventory with server-side search and paging; device identity with operator-owned
  metadata and trust state, evidence, duplicate candidates, merge and split; offline IEEE
  OUI vendor lookup (K-11) and rule-based classification suggestions as sourced facts (K-16).
- Local discovery of one administrator-configured subnet through the native Go agent
  (Windows/Linux); MAC-first reconciliation and online/offline tracking.
- Live traffic from the Windows ETW sensor or ntopng; geographic and 3D Traffic Map, with
  flows matched to devices by any current address (K-12).
- Neutral dark UI design system (`DESIGN.md`, decision 0002), top-bar shell and an
  attention-first Overview (K-13).
- Remote management foundation: enrolled agents with Ed25519 identities, Core-signed
  responses, heartbeats, device association, five audited read-only commands; agent
  installation as a Windows service or systemd unit (K-1), a copyable PowerShell
  enrollment command (K-20), reusable deployment keys with operator approval of new agents
  (K-22), and secret redaction plus configurable retention for command output (K-18).
- Releases: `v0.1.0` published on 2026-10-23. A `v*` tag builds reproducible agent
  binaries, signs the manifest with the key pinned in `internal/release/keys.go` and opens a
  draft release (K-7, decision 0003, `docs/RELEASING.md`).
- CI: Go tests, vet, PostgreSQL integration tests, API startup smoke test, frontend
  typecheck, map tests and build, image and agent artifact builds. Daily Dependabot.
- Development workflow: local Kanban board, this project memory, SessionStart hook and
  workflow tests (decision 0001).

See [ARCHITECTURE.md](ARCHITECTURE.md) for how it fits together and
[IMPLEMENTATION_PLAN.md](../IMPLEMENTATION_PLAN.md) for the data model, risks and forward plan.

## Active work

- No ticket in Doing or Review and no open pull request.
- Windows service installation (K-1) on a real elevated host is not verified yet.

## Blockers

- None in code. Privileged remote commands stay blocked until K-17 (approval gate) and K-2
  (privileged helper) land, by design.
- K-14 (per-sensor identity) waits for the maintainer's choice between options 1–3 on the
  ticket.

## Next priorities

Board order is the priority. All open tickets are `ready`; with K-22 landed, K-9, K-10,
K-15, K-17 and K-21 are unblocked.

1. EP-1 Production RMM milestone: K-17 approval gate → K-2 privileged helper → K-3
   system-management commands → K-4 RMM device page; K-5 remote commands and K-6 file
   management after K-2; K-19 remote agent updates; K-8 documentation and real-host
   verification last.
2. EP-2 Identity follow-ups: K-10 agent key rotation, K-9 enrolled identity for discovery,
   K-15 Core signing key rotation, K-14 per-sensor identity.
3. EP-3 Agent deployment: K-21 verified downloads and one-line install → K-23 Windows MSI.

Interactive sessions (MeshCentral), Discovery 2.0, local topology, Proxmox/Docker, change
intelligence and operations stay in `IMPLEMENTATION_PLAN.md` until planned in detail.

## Known limitations

- Discovery and the traffic sensor still use shared tokens; discovery report ingestion is
  not idempotent.
- No observed local topology; the 3D view uses derived /24 plates.
- No licence file, backup/restore, or retention policy beyond command output yet.
- Releases cover the agent only; Core, UI and sensor are still built from source with
  Docker Compose.
- pgx is held at 5.10 until pgxmock supports 5.11.
