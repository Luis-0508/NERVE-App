# Remote management (RMM): architecture proposal

Status: **proposal, nothing implemented.** This is the decision record for the next
major milestone. It compares three routes and states the security requirements any of
them must meet. Sequencing lives in [IMPLEMENTATION_PLAN.md](../IMPLEMENTATION_PLAN.md).

## Starting point

What exists and can be reused:

- Users, roles (`administrator`, `technician`, `read_only`) and hashed server-side sessions.
- An immutable `audit_log` (UPDATE/DELETE rejected by trigger), so far written only for
  authentication events.
- Unused `actions` / `action_runs` tables (requester, parameters, status, attempts) and
  `integrations` / `integration_credentials` (ciphertext, nonce, key version).
- A native Go agent (`cmd/nerve-agent`) that polls Core over HTTPS for discovery jobs, and a
  .NET Windows service (traffic sensor) with HMAC-signed, nonce-protected reports.

What is missing and blocks any route:

- **Agent identity.** All discovery agents share one global bearer token and the
  sensor shares one HMAC key. Anyone holding the token can impersonate every agent.
- **Device identity.** Discovery reconciles devices by MAC/IP, but there are no
  operator-confirmed identities, no merge/split workflow and no evidence view. An RMM
  endpoint must map to exactly one trusted NERVE device, or actions land on the wrong
  machine.
- **Authorization model for actions.** There is no per-device or per-group permission
  model and no capability registry.

## Option A: MeshCentral as the execution layer

NERVE stays the system of record and the operator UI. MeshCentral (Apache-2.0, Node.js
server with C MeshAgent for Windows/Linux/macOS) does agent connectivity and
interactive sessions.

| NERVE | MeshCentral |
| --- | --- |
| Inventory, discovery, identity, topology, health, events | Agent install, connection and relay |
| Policies, operator permissions, approvals | Remote desktop, terminal, file transfer |
| Action catalogue, confirmations, audit of intent and result | Command execution transport |
| Operator UI, deep links into sessions | Agent self-update channel, NAT traversal |

Integration mechanics:

- **API.** MeshCentral's own UI and `meshctrl` use an authenticated WebSocket control
  channel. Commands include `listdevices`, `deviceinfo`, `listevents`, `runcommand`
  (with `--reply` for output), `shell`, `upload` and `download`. NERVE would add an adapter
  under `internal/integrations/meshcentral` that keeps one server-side connection as a
  dedicated service account. The browser never talks to that channel.
- **Identity mapping.** MeshCentral node IDs (`node//…`) are stored as a
  `device_identities` row (`identity_type` would need a `meshcentral` value). A mapping is
  proposed by matching MAC/hostname, but an operator confirms it. NERVE never auto-binds
  a remote-control agent to a device on weak evidence.
- **Session handoff.** MeshCentral supports short-lived login tokens and URL parameters
  (`gotonode`, `viewmode`, `hide`) for embedding a device view in an iframe. Login tokens
  require `AllowLoginToken`. A login *key* can log in as any account, so it must stay
  server-side and must never reach the browser or logs. Preferred: NERVE mints a per-request,
  short-lived token for a per-operator MeshCentral account (or both trust the same OIDC
  provider), then deep-links or embeds. Avoid one shared MeshCentral admin identity for
  all operators: it erases operator attribution in MeshCentral's own log.
- **Permissions.** MeshCentral has device groups and per-user rights. NERVE policy is
  authoritative. MeshCentral accounts are provisioned with the minimum rights NERVE
  grants, so a bypass of NERVE (direct MeshCentral login) cannot exceed them.
- **Audit.** NERVE writes intent before the call and the result after it. MeshCentral
  events (`listevents`) are pulled and correlated, so sessions started inside MeshCentral
  are still visible in NERVE.
- **Deployment.** A separate container/host with its own TLS name. Agents must reach it
  directly (WebSocket/TLS), so it cannot hide entirely behind NERVE. Its admin UI should
  be restricted (VPN/allow-list/SSO), and NERVE should not proxy MeshCentral's whole web
  app through its own origin.

Costs and risks: a second stateful server with its own database, backups, upgrades and TLS;
a second user/permission model to keep in sync; an API that is mainly documented through
`meshctrl` and source rather than a versioned public contract; reliance on a project with
a small core maintainer group; a separate agent install alongside the NERVE agent.

## Option B: build remote management into the NERVE agent

Two very different scopes hide under "extend the agent".

**Tractable (weeks to a few months each, with care):** telemetry, capability discovery,
service list/start/stop, process list/kill, installed-software inventory, bounded
non-interactive commands/PowerShell with timeout and capped output, small file push/pull,
signed scripts from a server-side catalogue. These fit a request/response model over the
existing outbound HTTPS polling, plus long-polling or a WebSocket for latency.

**Hard (each a product in itself):**

- Remote desktop: screen capture (DXGI/Desktop Duplication, Wayland/X11, macOS
  ScreenCaptureKit), encoding, adaptive bitrate, multi-monitor, the secure desktop/UAC
  prompt, login-screen access, clipboard.
- Input injection: keyboard layouts, IME, secure attention sequence, session 0 vs user
  session separation on Windows.
- NAT traversal and relay: a relay service, TURN-like fallback, WebRTC or custom transport,
  bandwidth limits, abuse controls.
- Interactive terminal: PTY/ConPTY, resize, flow control, idle timeouts, transcript
  capture.
- Session handling: reconnect, resumption, concurrent viewers, consent prompts, session
  recording.
- File streaming: resumable transfers, integrity, quotas, path restrictions.
- Agent upgrades: signed packages, staged rollout, rollback, watchdog. A broken update
  can strand the whole fleet.
- Platform hardening: code signing, AV/EDR false positives, service recovery, privilege
  separation (unprivileged agent plus a narrow privileged helper).

Every one of these is security-critical code that runs as SYSTEM/root on every managed
host. For a small team, Option B for the hard list means years of work, and it creates a
persistent attack surface that must be maintained indefinitely.

## Option C (hybrid)

- **NERVE agent** stays the read-only/low-risk agent: discovery, telemetry, capability
  reporting, inventory, and later the tractable bounded actions behind the NERVE
  action pipeline.
- **MeshCentral** provides only the hard interactive pieces: remote desktop, interactive
  terminal, large file transfer, relay/NAT traversal. NERVE deep-links or embeds through
  short-lived, per-operator tokens.
- NERVE owns identity mapping, permissions, confirmations and the audit trail for both.
- The action pipeline is transport-agnostic (`actions.action_type` + an executor). A
  bounded command can later move from MeshCentral `runcommand` to the NERVE agent without
  changing the UI or audit model.

## Comparison

| | A: MeshCentral | B: native agent | C: hybrid |
| --- | --- | --- | --- |
| Time to first useful remote session | Short | Very long | Short |
| Remote desktop / relay quality | Mature | Must be built | Mature |
| Engineering effort | Adapter + identity + permissions | Very high | Adapter now, native actions gradually |
| Security surface owned by NERVE | Integration boundary, token handling | Everything, incl. privileged agent and relay | Integration boundary + bounded actions |
| Operational burden | Second server and agent | One stack | Second server and agent |
| Product coherence | Two UIs unless embedded | Single UI | Single UI for actions, embedded sessions |
| Vendor/project risk | High dependence | None | Contained: interactive features only |
| Exit path | Hard | n/a | Replace one executor at a time |

## Recommendation

**Take Option C, starting with MeshCentral for interactive sessions, and do not build remote
desktop, relay or interactive terminal in-house.** Build bounded, auditable actions in the
NERVE agent only after agent identity is fixed. Keep a short, explicit exit criterion: if
MeshCentral's API stability, maintenance or security posture becomes unacceptable,
only the interactive executor needs replacing.

Before any code, run a time-boxed spike. Stand up MeshCentral beside a disposable NERVE
stack. Confirm per-user login-token minting, `gotonode` embedding under NERVE's CSP,
`runcommand --reply` output limits, event retrieval for audit correlation, and the
real-world agent install and update flow on Windows and Linux.

Proposed order: (1) device identity and agent enrollment, (2) MeshCentral adapter,
read-only: list nodes, operator-confirmed mapping, presence on the device page, (3)
audited deep link/embed of sessions, (4) catalogue of confirmed bounded actions, (5)
selected actions moved to the NERVE agent.

## Security requirements (all options)

Assume the endpoint agent can eventually run privileged actions.

- **Unique per-agent identity**: per-agent key pair or client certificate, issued through
  a one-time, expiring enrollment token. No shared global agent secret. Revocation and
  rotation are supported.
- **Strong mutual authentication**: mTLS, or signed requests bound to the agent key, plus
  server certificate pinning in the agent.
- **Server-side authorization** for every action: operator role, device/group scope and
  action capability, checked by Core, never trusted from the browser.
- **Separation of read-only monitoring and administrative control**: distinct permissions,
  and ideally distinct agent capabilities that a host owner can disable locally.
- **Capability discovery**: agents report what they can do. Core refuses actions the
  agent did not advertise.
- **Short-lived, single-use command authorization**: each command carries an ID, target
  agent, expiry and server signature. The agent rejects expired, replayed or mis-targeted
  commands (nonce + timestamp, as the traffic sensor already does).
- **Idempotency** where the action allows it, keyed by action ID, so retries cannot
  double-execute.
- **Command timeout and output limits**, enforced on both agent and server, and
  truncation recorded in the result.
- **Explicit confirmation** for destructive actions (reboot, stop service, delete file,
  run script), showing target identity and evidence.
- **Explicit operator identity** on every action and session, carried into the
  execution layer, never collapsed into a shared service account where avoidable.
- **Immutable audit**: intent, approval, dispatch, result, duration, operator, source
  address and agent identity, in `audit_log`, with safe parameters only (no secrets, no
  full script bodies unless hashed and referenced).
- **Secure agent upgrades**: signed artifacts, version pinning, staged rollout, rollback,
  and no update from unauthenticated sources.
- **Least privilege**: unprivileged agent process with a minimal privileged helper; no
  host-network, raw-socket or Docker-socket privileges in Core.
- **No direct Docker socket exposure**: container actions go through a restricted socket
  proxy or mutually authenticated remote API.
- **No infrastructure credentials in the browser**: MeshCentral login keys, service
  passwords, Proxmox tokens and agent secrets stay server-side, encrypted at rest
  (`integration_credentials`), and are redacted from logs and API responses.
- **No proof-of-concept shortcuts**: no shared admin token in the browser, no disabled
  TLS verification, no unauthenticated agent endpoints, not even temporarily.
