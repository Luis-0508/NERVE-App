# Project state

Narrative snapshot of where NERVE stands. **The repository is authoritative:** if git, the
code, the task board or GitHub disagree with this file, trust them and correct this file.

- Last updated: 2026-10-16
- Verified against: `master` at `04b4fcd`

## What exists

- Go Core with PostgreSQL (checksummed migrations up to `000009`), static Next.js UI, Nginx
  proxy, Docker Compose; health/readiness endpoints and SSE.
- Sign-in with first-admin bootstrap, roles mapped to permissions, immutable audit log.
- Device inventory with server-side search and paging; device identity with operator-owned
  metadata and trust state, evidence, duplicate candidates, merge and split.
- Local discovery of one administrator-configured subnet through the native Go agent
  (Windows/Linux); MAC-first reconciliation and online/offline tracking.
- Live traffic from the Windows ETW sensor or ntopng; geographic and 3D Traffic Map.
- Remote management foundation: enrolled agents with Ed25519 identities, Core-signed
  responses, heartbeats, device association, five audited read-only commands.
- CI: Go tests, vet, PostgreSQL integration tests, API startup smoke test, frontend
  typecheck, map tests and build, image and agent artifact builds. Daily Dependabot.
- Development workflow: local Kanban board, this project memory, SessionStart hook and
  workflow tests (decision 0001).

See [ARCHITECTURE.md](ARCHITECTURE.md) for how it fits together and
[IMPLEMENTATION_PLAN.md](../IMPLEMENTATION_PLAN.md) for the data model, risks and forward plan.

## Active work

- **K-13** (Review): PR #70 `feat/K-13-ui-redesign`, UI redesign: neutral dark design system
  (`DESIGN.md`, decision 0002), top-bar shell and an attention-first Overview. Frontend only.
- **K-1** (Review): PR #54 `feat/agent-service-lifecycle`, agent service installation and
  lifecycle, the first PR of the production RMM milestone. It also adds the milestone's
  PR table to `IMPLEMENTATION_PLAN.md`. Windows service installation on a real elevated
  host is not verified yet.
- Workflow migration on `chore/unified-claude-workflow`; PR open for review. Maintenance
  work, no ticket.
- Open Dependabot PRs #51, #52, #55 to #59.

## Blockers

- None in code. Privileged remote commands stay blocked until K-2 (security hardening and
  privileged execution architecture) lands, by design.

## Next priorities

Board order is the priority; tickets after K-1 are `stub`s that need refining and approval.

1. EP-1 Production RMM milestone: K-2 security hardening → K-3 system-management commands
   → K-4 RMM device page; K-5 remote commands and K-6 file management after K-2; K-7 signed
   releases and updates after K-1; K-8 documentation and real-host verification last.
2. EP-2 Identity follow-ups: K-9 enrolled identities for discovery and sensor, K-10 key
   rotation, K-11 OUI lookup and classification, K-12 multi-address Traffic Map matching.

Interactive sessions (MeshCentral), Discovery 2.0, local topology, Proxmox/Docker, change
intelligence and operations stay in `IMPLEMENTATION_PLAN.md` until planned in detail.

## Known limitations

- Discovery and the traffic sensor still use shared tokens; discovery report ingestion is
  not idempotent.
- No observed local topology; the 3D view uses derived /24 plates.
- No releases, licence file, backup/restore or retention policy yet.
- pgx is held at 5.10 until pgxmock supports 5.11.
