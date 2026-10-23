# Changelog

All notable changes to NERVE are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Before tagging a release, rename
`[Unreleased]` to the version and date, for example `[0.1.0] - 2026-11-02`; the release
workflow takes the release notes from that section.

## [Unreleased]

## [0.1.0] - 2026-10-23

First versioned release of NERVE: a self-hosted network inventory, live traffic map and
remote-management foundation. Agents are released as signed, reproducible builds for
windows/amd64, linux/amd64 and linux/arm64.

### Added

- **Platform:** Go Core with PostgreSQL (checksummed migrations), static Next.js UI, Nginx
  proxy and Docker Compose setup with health and readiness endpoints and live updates over
  server-sent events.
- **Access control:** sign-in with first-admin bootstrap, role-based permissions and an
  immutable audit log.
- **Inventory:** device list with server-side search, filters and paging; operator-owned
  identity metadata and trust state with provenance; identity evidence; duplicate
  candidates, merge and split.
- **Discovery:** local subnet discovery through the native Go agent on Windows and Linux,
  with MAC-first reconciliation and online/offline tracking.
- **Traffic Map:** live traffic from the Windows ETW sensor or ntopng, shown on a
  geographic map and an interactive 3D view. Flows are matched to a device by any of its
  current addresses, not only its primary IP.
- **Remote management foundation:** enrolled agents with per-agent Ed25519 identities,
  Core-signed responses, heartbeats, capabilities, device association and five audited
  read-only commands.
- **Agent setup:** installation as a Windows service or systemd unit, enrollment from
  `NERVE_AGENT_ENROLLMENT_TOKEN` during service install, and a copyable PowerShell
  enrollment command in the agent settings.
- **UI:** neutral dark design system and an attention-first Overview page.
- **Releases:** signed agent releases with a `manifest.json` holding the SHA-256 of every
  file, an Ed25519 signature, an SPDX SBOM, reproducible builds and
  `nerve-agent verify-release`.

### Security

- Next.js upgraded past the `next/og` remote-code-execution advisory (16.3.8, now 16.4.2).
- Privileged remote commands are not available in this release by design; they stay
  blocked until the privileged-execution hardening lands.

### Known limitations

- Discovery and the traffic sensor still authenticate with shared tokens, and discovery
  report ingestion is not idempotent.
- No observed local topology yet; the 3D view groups devices into derived /24 plates.
- No backup/restore or data retention policy yet.
