# Project state

Narrative snapshot of where NERVE stands. **The repository is authoritative:** if git, the
code or GitHub issues disagree with this file, trust them and correct this file.

- Last updated: 2026-10-29
- Verified against: `master` at `41360b5`

## What exists

- Go Core with PostgreSQL (checksummed migrations up to `000016`), static Next.js UI, Nginx
  proxy, Docker Compose; health/readiness endpoints and SSE.
- Sign-in with first-admin bootstrap, roles mapped to permissions, immutable audit log.
- Device inventory with server-side search and paging; device identity with operator-owned
  metadata and trust state, evidence, duplicate candidates, merge and split; offline IEEE
  OUI vendor lookup (K-11) and rule-based classification suggestions as sourced facts (K-16).
- Local discovery of one administrator-configured subnet through the native Go agent
  (Windows/Linux); MAC-first reconciliation and online/offline tracking. Enrolled agents
  run the worker with their signed identity (#90); the shared token is deprecated.
- Live traffic from the Windows ETW sensor or ntopng; geographic and 3D Traffic Map, with
  flows matched to devices by any current address (K-12).
- Neutral dark UI design system (`DESIGN.md`, decision 0002), top-bar shell and an
  attention-first Overview (K-13).
- Remote management foundation: enrolled agents with Ed25519 identities, Core-signed
  responses, heartbeats, device association, five audited read-only commands; agent
  installation as a Windows service or systemd unit (K-1), a copyable PowerShell
  enrollment command (K-20), reusable deployment keys with operator approval of new agents
  (K-22), agent-initiated key rotation with crash-safe recovery (K-10), and secret
  redaction plus configurable retention for command output (K-18).
- Releases: `v0.1.0` published on 2026-10-23. A `v*` tag builds reproducible agent
  binaries, signs the manifest with the key pinned in `internal/release/keys.go` and opens a
  draft release (K-7, decision 0003, `docs/RELEASING.md`).
- CI: Go tests, vet, PostgreSQL integration tests, API startup smoke test, frontend
  typecheck, map tests and build, image and agent artifact builds. Daily Dependabot.
- Development workflow: planned work in GitHub issues and milestones (decision 0005), this
  project memory, SessionStart hook and workflow tests (decision 0001).

See [ARCHITECTURE.md](ARCHITECTURE.md) for how it fits together and
[IMPLEMENTATION_PLAN.md](../IMPLEMENTATION_PLAN.md) for the data model, risks and forward plan.

## Active work

- #111 (#104 read-only MeshCentral provider, migration `000017`) is in review; its UI was not
  checked in a browser. It was verified against a disposable MeshCentral 1.3.1 server, but
  not with a real MeshAgent reporting network info.
- The #99 approval dialog (merged in #110) was not checked in a browser.
- Windows service installation (K-1) on a real elevated host is not verified yet.

## Blockers

- None in code. Privileged remote commands stay blocked until #91 (privileged helper) lands,
  by design.
- #97 (per-sensor identity) waits for the maintainer's choice between options 1–3 on the
  issue (#97).

## Next priorities

This order is the priority. Issues migrated from the board keep their K-n ID in the first
line; #91, #98 and #101 are unblocked; #105 waits for #111.

1. Production RMM milestone (hybrid, decision 0006), two parallel tracks:
   - native path: #99 approval gate (done, #110) → #91 privileged helper (fixed allowlist) → #92
     system-management commands → #93 RMM device page;
   - MeshCentral: #104 read-only provider → #105 encrypted integration credentials → #106
     audited remote sessions;
   then #100 remote agent updates; #96 documentation and real-host verification last.
2. Identity follow-ups: #98 Core signing key rotation, #97 per-sensor identity.
3. Agent deployment: #101 verified downloads and one-line install → #102 Windows MSI.

Discovery 2.0, local topology, Proxmox/Docker, change intelligence and operations stay in
`IMPLEMENTATION_PLAN.md` until planned in detail.

## Known limitations

- The traffic sensor still uses a shared token, and unenrolled discovery workers may still
  use the deprecated one (`NERVE_DISCOVERY_LEGACY_TOKEN`); discovery report ingestion is not
  idempotent.
- No observed local topology; the 3D view uses derived /24 plates.
- No licence file, backup/restore, or retention policy beyond command output yet.
- Releases cover the agent only; Core, UI and sensor are still built from source with
  Docker Compose.
- pgx is held at 5.10 until pgxmock supports 5.11.
