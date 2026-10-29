# 0006. MeshCentral for interactive remote sessions

- Status: Accepted
- Date: 2026-10-27

## Context

The Production RMM milestone planned every remote capability on NERVE's own agent: a
privileged helper (#91), system-management commands (#92), bounded shell and PowerShell
commands with transcripts (#94) and file management in authorized roots (#95). Remote
desktop was not planned at all. Interactive terminal, file transfer and remote desktop
across Windows, Linux and macOS are a large, security-sensitive surface that mature
open-source products already cover. MeshCentral (Apache-2.0) is one of them, and
`docs/REMOTE_MANAGEMENT.md` already describes it as an optional provider behind
`internal/remote.Provider`, with facts checked against its source.

## Decision

- **MeshCentral provides interactive sessions:** remote desktop, terminal and file
  transfer. It runs as a separate service on its own origin, with its own MeshAgent on
  endpoints, and is reached through a MeshCentral provider behind `remote.Provider`.
- **NERVE keeps the core model:** device identity, inventory, enrollment, permissions and
  scopes, device trust, audit and the operator UI. MeshCentral nodes become `meshcentral`
  device identities only after operator confirmation.
- **The native privileged path shrinks to a fixed allowlist** of audited actions run by the
  privileged helper with Core-signed approvals: service restart/stop, process termination,
  reboot/shutdown, log retrieval and applying agent updates. Free-form shell, PowerShell,
  scripts and file operations are not built natively; #94 and #95 are closed.
- **Handoffs are NERVE-authorized and audited.** NERVE provisions one MeshCentral account
  per operator with minimal rights derived from NERVE scopes, writes an audit entry before
  every handoff, and hands over through a short-lived login link in a new tab. Embedding
  comes later, if at all.
- Delivery: a read-only provider spike (#104), encrypted integration credentials (#105),
  then audited sessions (#106), in parallel with the native path (#99 → #91 → #92 → #93).

## Alternatives considered

- **Build everything natively** (the previous plan): full control and one trust model, but
  shell, files and remote desktop would take many pull requests to reach what MeshCentral
  already does, and remote desktop was not planned at all.
- **MeshCentral for everything, including privileged actions**: least code, but its
  `runcommands` and rights model bypass NERVE's per-command approval, trusted-device
  policy and scopes.
- **Other session products**: RustDesk (AGPL-3.0) covers desktop well but not terminal and
  file management as an API, and its licence would constrain NERVE's still unchosen
  licence; Tactical RMM is source-available, not open source, and itself builds on
  MeshCentral.

## Consequences

- Remote desktop, terminal and file transfer arrive much earlier, and three planned native
  capabilities are not built.
- A second service and a second endpoint agent must be deployed, updated and secured.
- Inside a MeshCentral session, MeshCentral's rights apply, not NERVE's per-command
  approval. Mitigations: per-operator accounts with minimal rights, NERVE permissions and
  scopes checked before each handoff, NERVE's own audit entry, and MeshCentral session
  limits. MeshCentral events are useful for correlation but not a complete audit.
- The MeshCentral login-token key is equivalent to full MeshCentral administrator access;
  it may only be stored in encrypted integration credentials (#105).
- Replacing MeshCentral later means replacing one provider adapter.
