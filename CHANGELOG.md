# Changelog

All notable changes to NERVE are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Before tagging a release, rename
`[Unreleased]` to the version and date, for example `[0.1.0] - 2026-11-02`; the release
workflow takes the release notes from that section.

## [Unreleased]

First versioned release.

### Added

- Go Core with PostgreSQL, Next.js UI, Nginx proxy and Docker Compose setup.
- Sign-in with first-admin bootstrap, role-based permissions and an immutable audit log.
- Device inventory with search, operator-owned identity and trust state, evidence,
  duplicate candidates, merge and split.
- Local subnet discovery through the native agent.
- Live traffic from the Windows ETW sensor or ntopng, with a geographic and a 3D Traffic Map.
- Remote management foundation: enrolled agents with Ed25519 identities, Core-signed
  responses, heartbeats, device association and five audited read-only commands.
- Agent installation as a Windows service or systemd unit.
- Signed agent releases: `manifest.json` with SHA-256 per file, an Ed25519 signature, SPDX
  SBOM, reproducible builds and `nerve-agent verify-release`.
