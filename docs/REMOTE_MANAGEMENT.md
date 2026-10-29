# Remote management (RMM)

Status: **foundation implemented** (October 2026): per-agent enrollment and identity,
heartbeat and capabilities, evidence-based device association, and an audited command
pipeline with five read-only commands, with both directions of the agent channel
authenticated (agent request signatures and Core response signatures). Interactive sessions (desktop, terminal, files)
and privileged commands are **not** implemented. Agent installation is described in
[Agent setup](AGENT_SETUP.md); sequencing lives in
[IMPLEMENTATION_PLAN.md](../IMPLEMENTATION_PLAN.md).

## Architecture

NERVE uses a **hybrid** model. NERVE owns device identity, inventory, agent enrollment and
authentication, capabilities, telemetry, authorization, command requests and results,
confirmations, audit and the operator UI. MeshCentral provides remote desktop,
interactive terminal, file transfer and relay as a **provider** behind a NERVE abstraction,
never as the core model ([decision 0006](DECISIONS/0006-meshcentral-for-interactive-sessions.md)).
NERVE's own privileged path is limited to a fixed allowlist of audited actions.

```text
Browser ──session──▶ /api/v1/agents, /devices/{id}/remote, /remote/commands   (permissions)
                         │
                   internal/remote ── Provider registry ── NativeProvider (nerve-agent)
                         │                              └─ MeshCentral adapter (planned, #104)
                   remote_commands ◀── claim/start/result ── /api/v1/agent/* ◀──Ed25519── nerve-agent
                         │
                     audit_log (immutable)
```

- `internal/agentproto`: wire protocol shared by Core and the agent (request signing,
  messages, capability catalogue).
- `internal/agents`: enrollment, credentials, signed-request authentication, heartbeat,
  device association, revoke/re-enroll/remove.
- `internal/remote`: provider-neutral domain: `Provider` registry, command policies and
  the command pipeline.
- `internal/authz`, `internal/audit`: permissions and the audit writer/reader.

## Agent identity and enrollment

```text
operator (agents.manage) ── POST /api/v1/agent-enrollments ──▶ token, shown once
                                                                    │
nerve-agent enroll ── generates Ed25519 key pair ── POST /api/v1/agent/enroll
                      (token + public key + host facts, signed with the new key)
                                                                    │
Core: lock enrollment, verify digest/expiry/single use ──▶ agent ID + credential ID
```

- **Enrollment tokens** look like `nerve-enroll-v2.<enrollment id>.<secret>.<Core key
  fingerprint>` with a 256-bit secret. The fingerprint (base64url SHA-256 of Core's public
  key) lets the agent authenticate Core during and after enrollment; see below. Core stores only `SHA-256(secret)`. They expire after 5 minutes to 24
  hours (default 60 minutes) and are consumed exactly once under `SELECT … FOR UPDATE`.
  Operators can revoke unused tokens. Rejected attempts against an existing enrollment
  (wrong secret, expired, revoked, already used, key reuse) are audited. Attempts with
  unknown enrollment IDs are not, so anonymous callers cannot grow the immutable log.
  The endpoint is rate-limited per client address.
- **Deployment keys** (`nerve-deploy-v1.<key id>.<secret>.<Core key fingerprint>`) enroll
  many endpoints with one secret, for a GPO, Intune or scripted rollout. They are created,
  listed and revoked under Settings → Management agents (`/api/v1/agent-deployment-keys`,
  `agents.manage`). A key has a name, a use limit (1–10,000) and a lifetime of 1 hour to
  90 days. Like enrollment tokens, only `SHA-256(secret)` is stored, and the key is shown
  once. Each enrollment creates a new agent and counts one use under `SELECT … FOR UPDATE`,
  so concurrent enrollments never exceed the limit; PostgreSQL also enforces the limit and
  makes the key terms immutable and revocation final. Revoking a key stops new
  enrollments only; agents it already enrolled keep their own credentials.
- **Approval.** With the default `manual` approval, an agent enrolled by a deployment key
  starts as `pending`. It authenticates and heartbeats (so the operator sees its host
  facts, key fingerprint and online state), but Core queues no commands for it and links
  it to no device. **Approve** (`POST /agents/{id}/approve`) makes it `active` and runs
  device association; **Reject** removes it. A key with `automatic` approval enrolls
  active agents directly: anyone holding it can then add an agent until it is revoked,
  expires or runs out of uses.
- **Rejection messages.** A refused deployment key whose secret is correct gets the reason
  (revoked, expired, use limit reached with the limit, or a different Core key), so the
  person rolling out sees what to fix. A wrong secret or unknown key ID gets the generic
  rejection, so the key's state is never revealed without the secret. The audit log records
  the reason in every case.
- **Proof of possession.** The enroll request is signed with the private key whose public
  key it registers. A token alone cannot register someone else's key.
- **Credential.** Each agent has its own Ed25519 key pair. The private key never leaves
  the endpoint. PostgreSQL stores only the 32-byte public key and its SHA-256 fingerprint,
  so a database leak yields nothing that can authenticate as an agent. A unique index
  allows at most one active credential per agent, and triggers make key material
  immutable and revocation final.
- **Agent ID** is a UUID assigned by Core. It stays stable across re-enrollment.

### Why Ed25519 instead of a hashed bearer token

A random bearer token stored as a SHA-256 digest would also keep plaintext credentials
out of PostgreSQL, and it is simpler. It was rejected because NERVE's default deployment
is plain HTTP on a LAN: a bearer token is sent with every request, so one captured
request allows permanent impersonation. With request signatures, no reusable secret ever
crosses the network, and a captured request is useless after its nonce is recorded. The
implementation uses only Go standard-library primitives (`crypto/ed25519`, SHA-256), and
the canonical-string scheme mirrors the existing HMAC-signed traffic-sensor reports. No
custom cryptography is involved.

### Signed requests

Every request to `/api/v1/agent/*` (except enroll, which is self-signed) carries
`X-NERVE-Credential`, `X-NERVE-Timestamp`, `X-NERVE-Nonce` (16 random bytes) and
`X-NERVE-Signature` over:

```text
NERVE-AGENT-SIG-V1\n<credential id>\n<METHOD>\n<route below /api/v1, with query>\n<unix time>\n<nonce hex>\n<sha256(body) hex>
```

Core rejects timestamps more than ±2 minutes away (and returns its time in
`X-NERVE-Server-Time`). It verifies the signature **before** any write, then records the
nonce in `agent_request_nonces` for 4 minutes, so a replay inside the window fails. The
maintenance loop prunes expired nonces.

### Core authentication (agent side)

Agents also authenticate Core. With a key configured, Core signs **every** response on `/api/v1/agent/*`,
including errors, with an Ed25519 key from `NERVE_CORE_SIGNING_KEY`:

```text
NERVE-CORE-SIG-V1
<core key id>
<the request's X-NERVE-Signature>
<HTTP status>
<sha256(response body) hex>
```

- The agent learns which key to trust **out of band**: the enrollment token carries the
  key's fingerprint, and the operator copies the token from the NERVE UI. The agent checks
  the key in the enroll response against that fingerprint, verifies the signature and
  pins the key in its identity file. A network attacker cannot substitute a key, even if
  enrollment runs over plain HTTP.
- Because the signature covers the request's own signature, a response is valid only for
  the single request it answers. Captured responses cannot be replayed for other requests,
  commands cannot be forged or altered, and a fake "credential revoked" or "cancelled"
  answer is rejected.
- The agent treats an unverifiable response as a transport failure: it never executes a
  command, records a result as delivered, or pauses because of one.
- Core refuses agent enrollment and the whole agent API (HTTP 503) when no signing key is
  configured, rather than running unauthenticated. That 503 answer is the one unsigned
  response (there is no key to sign it with); agents reject it as unverifiable and log the
  route and HTTP 503, so check the API logs. `setup.bat` generates the key
  (`start.bat` runs setup, so existing installations get one on their next start).
  Compose passes it only to the API container, and it is never stored in PostgreSQL.
- Tokens that pin a different Core key (for example issued before the key changed) are
  rejected and audited as `core_key_mismatch`. Changing the key requires re-enrolling
  every agent; rotation without re-enrollment is not implemented.

### Separation of browser and agent APIs

| | Operator API (`/api/v1/agents`, `/agent-enrollments`, `/remote/*`, `/devices/{id}/remote`) | Agent API (`/api/v1/agent/*`) |
| --- | --- | --- |
| Authentication | Browser session cookie | Ed25519 request signature |
| Rejects | Agent signatures (no cookie means 401) | Any request with `Origin` or `Sec-Fetch-Site`, and session cookies (ignored, so 401) |
| Authorization | Explicit permission per route | Scoped to the authenticated agent's own rows |

The legacy discovery worker still uses the shared local `NERVE_DISCOVERY_AGENT_TOKEN`.
That token grants nothing on either API above.

### Lifecycle

| Action | Effect |
| --- | --- |
| **Approve** (`POST /agents/{id}/approve`) | Only for `pending` agents enrolled by a deployment key. Status becomes `active`, device association runs, and the approval is audited. |
| **Revoke** (`POST /agents/{id}/revoke`) | Active credential revoked. Agent status becomes `revoked`. Its queued, claimed and running commands are cancelled in the same transaction. The agent receives `agent_credential_revoked` and pauses polling. |
| **Rotate** (agent-initiated) | `nerve-agent rotate-key`, or automatically after `credentialMaxAgeDays`. Same agent ID, new key; the old credential is revoked as `rotated`. No operator token is needed. See [Key rotation](#key-rotation). |
| **Re-enroll** | Create an enrollment with `agentId`, then run `nerve-agent enroll --force`. Same agent ID, new key. The old credential is revoked as `reenrolled`. Use it for a reinstall, a lost identity file or a revoked agent. |
| **Remove** (`POST /agents/{id}/remove`) | Final. Credentials are revoked, the device association is cleared and the agent can never authenticate or be re-enrolled. The row is kept because audit records and command history reference it. |

### Key rotation

An enrolled agent replaces its own Ed25519 key with
`POST /api/v1/agent/credential/rotate`:

1. The agent generates a new key and a credential UUID and saves them in
   `identity.pending.json` before it contacts Core.
2. The request is signed with the **current** key. Its body carries the new public key,
   the proposed credential ID, a timestamp and a proof: the new key's signature over
   agent ID, current credential ID, proposed credential ID, new public key and timestamp
   (`NERVE-AGENT-ROTATE-V1`). Proofs outside the two-minute clock skew are refused.
3. In one transaction Core revokes the current credential as `rotated`, stores the new one
   under the proposed ID and audits `agent.credential.rotated` with the agent as actor.
   The answer is Core-signed.
4. Only after verifying that answer does the agent write `identity.json` and delete the
   pending file.

A crash or a lost answer leaves the pending file behind. On the next start (and then once
a minute) the agent asks Core with `POST /agent/identity` which key it accepts: the
pending one (Core committed: the agent promotes it) or the current one (Core never stored
the new key: the agent discards it). While Core is unreachable the agent keeps both and
starts no new rotation. Revoked and removed agents cannot rotate; `pending` agents can and
stay pending. A public key or credential ID that is already registered is refused.

Settings → Management agents shows each key's age; its tooltip shows how often the agent
rotated and when it last did.

## Heartbeat, capabilities and device association

The agent sends a signed heartbeat every 30 seconds with host facts: hostname, OS and
version, architecture, agent version, interface MAC and IP addresses, and a domain-separated
SHA-256 of the machine ID (the raw ID never leaves the host). It also sends its capabilities
and, since the service milestone, its runtime: run mode (`windows_service`, `systemd`,
`foreground`), process start time and host boot time (stored in `agents.run_mode`,
`process_started_at`, `host_boot_at`; older agents omit them).

The API reports a `connection` state per agent:

| `connection` | Meaning |
| --- | --- |
| `online` | An authenticated request in the last 90 seconds, and no later shutdown report |
| `stopped` | The agent's last heartbeat announced a graceful shutdown (`agents.last_shutdown_at`) |
| `offline` | Silent for more than 90 seconds without a shutdown report (crash, network loss, power loss) |
| `never_seen` | Enrolled, but no authenticated request yet |
| `inactive` | Revoked or removed |

The shutdown report is a signed heartbeat like any other, so it cannot be forged on the
network. `online` stays `false` while the shutdown is newer than the last request.

**Capabilities** come from a shared catalogue (`internal/agentproto/capabilities.go`):

| Capability | Risk | Executable today |
| --- | --- | --- |
| `system.info`, `system.metrics`, `process.list`, `service.list`, `software.list` | read-only | yes |
| `command.shell`, `command.powershell`, `script.run`, `system.reboot` | privileged | no (no policy) |
| `file.transfer`, `remote.desktop`, `remote.terminal` | interactive | no (provider not implemented) |

Agents advertise only what works on their host. For example, Linux reports
`service.list` only with systemd and `software.list` only with dpkg or rpm. Host owners
can switch capabilities off with `NERVE_AGENT_DISABLED_CAPABILITIES`. Core discards
unknown names and refuses commands that the agent did not advertise.

**Device association** proposes an existing NERVE device from evidence. It never creates
or merges devices:

| Evidence | Result |
| --- | --- |
| A reported MAC matches exactly one device (via `device_identities` or interfaces) | `automatic` |
| A MAC matches several devices | `candidate` with a note |
| Only hostname and/or IP matches | `candidate` (weak) |
| The device already has another live agent (unique index) | `candidate` with a note |
| Operator confirms or picks a device at enrollment | `confirmed` |
| Operator says "no device" | `dismissed` |

Evidence (signal type and value per candidate device) is stored on the agent and shown
in the API. Operator decisions (`confirmed`, `dismissed`) are never overridden
automatically. Unresolved agents are re-evaluated on every heartbeat, so a device
discovered later is still linked. Resolved agents are re-evaluated only when their
facts change.

Discovery and remote management converge on one device record. An associated agent is
recorded as an `agent` row in `device_identities`. Its machine ID, MACs, hostname and
addresses become identity evidence, and its hostname, OS and architecture become sourced
facts on the device (see [Device identity](DEVICE_IDENTITY.md)). The association survives
IP changes, moves with the device when operators merge records, and is handled explicitly
(keep, move, detach) when they split one. An agent that is moved or removed takes its
evidence with it.

## Device trust

Every device has an operator-owned trust state (`unknown`, `reviewed`, `trusted`,
`blocked`). Discovery never changes it, and merges never raise it.

| Device trust | Remote management today | Intended for privileged actions |
| --- | --- | --- |
| `blocked` | Commands are refused (`device_blocked`) | Refused |
| `unknown` / `reviewed` | Read-only commands allowed | Refused: inventory and read-only only |
| `trusted` | Read-only commands allowed | Eligible, subject to the remaining prerequisites below |

Agents that are not associated with any device can run read-only commands. Privileged
actions will additionally require an associated device in the `trusted` state.

## Command pipeline

```text
operator ── POST /agents/{id}/commands ──▶ policy + permission + capability check ──▶ queued (audit: created)
agent    ── POST /agent/commands/claim (long poll ≤25 s) ──▶ claimed     (audit: claimed)
agent    ── POST /agent/commands/{id}/start ──▶ running                   (audit: started; refused if cancelled)
agent    ── POST /agent/commands/{id}/result ──▶ succeeded|failed|timed_out (audit: completed; output → results table)
sweeper  ── queued past expiry ──▶ expired; claimed/running past timeout+60 s ──▶ timed_out
operator ── POST /remote/commands/{id}/cancel ──▶ cancelled
```

- **At-most-once execution.**
  - Claiming is one atomic `UPDATE … FOR UPDATE SKIP LOCKED`, scoped to the authenticated
    agent, and no transition returns a command to `queued`.
  - The agent also refuses command IDs it has already executed, and commands addressed
    to another agent.
- **Idempotent results.**
  - An identical resubmission is acknowledged as a duplicate, so a retry after a lost
    response is safe.
  - A different result for a finished command is refused (409).
  - A result from another agent is refused (404) and audited.
- **Bounds.**
  - Per-type timeouts (default 20–60 s, max 1–2 min) are enforced by the agent (context
    deadline) and by Core (sweeper).
  - Per-type output limits range from 16 KiB to 1 MiB, enforced by both. The agent
    truncates list outputs and marks them `truncated`. Request bodies are capped at 2 MiB.
  - Parameters are normalized by a per-type schema and capped at 4 KiB.
  - Expiry is 30 s to 1 h (default 10 min).
  - Each agent has at most 20 active commands.
- **Database invariants.** Triggers enforce the allowed transitions, terminal finality,
  immutable request fields and claims, no deletion of commands, and write-once results.
  CHECK constraints keep timestamps consistent with the state.
- **Redaction.** Before storage, `internal/redact` replaces common secrets in stdout,
  stderr, the failure reason and every string value of structured output with
  `[redacted:<kind>]`: private key blocks, credentials in URLs, bearer tokens, AWS access
  keys, and values of `password=`, `pwd:`, `secret=`, `api_key=`, `token=` and similar
  assignments (key names stay readable). The agent redacts before upload; Core applies the
  same rules again on receipt, so an outdated or modified agent cannot store what the
  current rules catch. The rules are idempotent, so the result digest is the same either way.
  The counts per kind are stored with the result, shown under the output and recorded in
  the `remote.command.completed` audit entry. Redaction is pattern-based: a secret in an
  unrecognized format is stored as is.
- **Retention.** Results are pruned after `NERVE_COMMAND_RESULT_RETENTION` (default `30d`;
  a Go duration such as `720h` or whole days, from 1 hour to 3650 days). Command rows and
  audit records remain, and the UI says the output was removed by retention.
- **Access to output.** Every operator with `remote.view` sees the command history. The
  output of any command that is not read-only is shown only to operators who also hold
  `remote.execute.privileged`; others see that it was withheld.
- **Transport.** Polling and long polling over the same HTTP(S) origin as the UI, through
  Nginx. The agent tolerates network loss and Core restarts (jittered backoff, 2 s to
  60 s), its own restarts (identity on disk; after a crash an in-flight command times out on Core,
  after a graceful stop it is reported as `agent_stopped`), and
  duplicate or late submissions.

The five read-only collectors read OS state (procfs, statfs, kernel32, the service control
manager with enumerate rights only, the uninstall registry keys) or run a fixed program
with fixed arguments (`systemctl`, `dpkg-query`, `rpm`). No operator-supplied text is
executed. Process command lines and environments are deliberately not collected because
they often contain secrets.

## Authorization

Permissions live in `permissions` and `role_permissions`, and every check is server-side:

| Permission | Administrator | Technician | Read-only |
| --- | --- | --- | --- |
| `agents.view` (agents, status, capabilities, device remote state) | ✓ | ✓ | ✓ |
| `remote.view` (command history and results) | ✓ | ✓ | |
| `remote.execute.read_only` (queue and cancel read-only commands) | ✓ | ✓ | |
| `agents.manage` (enroll, revoke, remove, associate) | ✓ | | |
| `remote.execute.privileged` (reserved; no command uses it yet) | ✓ | | |
| `audit.view` (`GET /api/v1/audit`) | ✓ | | |
| `discovery.manage` (discovery target and scans) | ✓ | | |
| `devices.manage` (device name, type, tags, owner, location, notes) | ✓ | ✓ | |
| `devices.trust` (device trust state) | ✓ | | |
| `devices.merge` (merge, split, dismiss duplicate candidates) | ✓ | | |

A user's permissions are the union over all their roles. Changing a role is a data
change, not a code change. Denied state-changing requests are audited as `authz.denied`.

## Audit

`audit_log` (immutable by trigger) now has `actor_agent_id` next to `actor_id`. Recorded
actions:

- Agents and enrollment:
  - `agent.enrollment.created`, `agent.enrollment.revoked`, `agent.enrollment.completed`
    (actor: agent)
  - `agent.enrollment.rejected`
  - `agent.credential.revoked`, `agent.removed`
  - `agent.association.changed` (operator), `agent.association.evaluated` (agent)
- Commands:
  - `remote.command.created` (operator); `remote.command.claimed`, `started` and
    `completed` (agent)
  - `remote.command.cancelled`, `remote.command.expired`, `remote.command.timed_out`
  - `remote.command.result_rejected`
- `discovery.target.saved`, `discovery.scan.requested`, `discovery.scan.cancelled` (this
  closes the earlier discovery audit gap)
- `authz.denied`

Audit parameters hold only identifiers, types, sizes, digests and outcomes. They never
hold tokens, keys or command output. Results live in `remote_command_results` and are
referenced by command ID.

## Current limitations

- **No confidentiality on plain HTTP.** Both directions are authenticated (agent request
  signatures, Core response signatures), so commands cannot be forged and responses cannot
  be redirected to another request. But NERVE does not encrypt payloads itself: on plain
  HTTP, anyone on the network path can **read** command results, host facts and
  inventory data, and can block traffic. **Use HTTPS (a reverse proxy with a trusted
  certificate) for agents outside a trusted network.** Enforcing HTTPS for agents that may
  run privileged capabilities is a **blocker for privileged remote actions**.
- No Core signing-key rotation and no signed agent updates. The agent installs as a
  Windows service or systemd unit with an unprivileged account (see
  [Agent setup](AGENT_SETUP.md#install-as-a-service)); there is no MSI package yet.
- The discovery worker and the Windows traffic sensor still use their shared local
  secrets. They should move to enrolled identities (see the plan).
- Cancelling a running command only discards its result. Read-only collectors have no
  side effects, and the agent is not interrupted. A command interrupted by an agent
  shutdown is reported as failed with `agent_stopped`.
- One API instance is assumed for instant wake-up. With several instances, claims fall back
  to a 3-second re-check.

## MeshCentral integration (planned provider)

Decided in [0006](DECISIONS/0006-meshcentral-for-interactive-sessions.md); delivered by
#104 (read-only provider and confirmed identities), #105 (encrypted integration
credentials) and #106 (per-operator accounts and audited session handoff).

The `internal/remote.Provider` registry is the seam. A MeshCentral adapter
(`internal/integrations/meshcentral`) would register as provider `meshcentral` and
report the same catalogue capabilities (`remote.desktop`, `remote.terminal`,
`file.transfer`). `GET /api/v1/devices/{id}/remote` and the UI would show them next to
the native agent without knowing which provider serves them. Interactive sessions get
their own optional provider interface (for example `OpenSession(device, kind, operator)`
returning a short-lived handoff), so providers without sessions need no stubs.
Replacing MeshCentral later means replacing that adapter.

### Mapping

```text
NERVE device ◀─ device_identities(agent)        ─ NERVE agent (enrolled, Ed25519)
NERVE device ◀─ device_identities(meshcentral)* ─ MeshCentral node "node/<domain>/<id>"
```

\* `identity_type` needs a `meshcentral` value (new migration). The MeshCentral node ID is
`node/<domain>/<id>` (`node//…` only for the default domain). The `<id>` is derived from
the MeshAgent's public key (a SHA-384 hash), so a **reinstall that generates a new agent
key produces a new node ID**. The mapping must be confirmed by an operator, matched on
MAC address and hostname evidence, and re-confirmed after a reinstall. Never auto-bind
on weak evidence.

### Verified facts and corrections to the earlier proposal

These were checked against MeshCentral `master` source in October 2026. Re-verify them
before implementing.

- **Control channel.** Clients use the authenticated WebSocket `/control.ashx` (prefixed
  for path-based domains). The earlier text listed *meshctrl CLI* commands. The WebSocket
  actions are different:
  - `listdevices` sends `nodes`
  - `deviceinfo` combines `nodes`, `getnetworkinfo`, `lastconnect` and `getsysinfo`
  - `listevents` sends `events` (filters `nodeid`, `userid`, `limit`)
  - `runcommand --reply` sends `runcommands` with `nodeids`, `cmds`, `reply`,
    `responseid`
- **Service authentication.** Username and password (plus 2FA token), or the server's
  login-token key.
  - `meshcentral --logintokenkey` exports the cookie-encryption key. Whoever holds it can
    mint login cookies **for any user**, so it is equivalent to full administrator access.
    If NERVE ever stores it, it belongs in encrypted `integration_credentials`, never in
    logs or the browser.
  - The domain `loginKey` / `?key=` setting is a separate access gate, not an
    impersonation key.
- **Short-lived operator access.**
  - `createLoginToken` only creates tokens for the *logged-in* user, so it cannot mint
    tokens for other operators.
  - A server holding the login-token key can mint a per-user cookie with an `expire`
    value (in minutes).
  - The cookie expiry bounds only the login step. **The resulting session is not capped
    to that lifetime.** Session length must be limited by MeshCentral session settings,
    and NERVE must audit the handoff.
  - Whether `AllowLoginToken` is enforced on the URL-login path could not be confirmed.
    Test it in a spike.
- **Embedding.**
  - `gotonode`, `viewmode`, `hide` and `?login=<cookie>` exist. `hide` only changes the
    display, not permissions.
  - The default is `X-Frame-Options: sameorigin`. Embedding needs `AllowFraming` and
    `allowedFramingOrigins`.
  - An embedded MeshCentral UI talks to MeshCentral **directly from the browser**,
    including separate relay WebSockets for terminal and files. The earlier claim that
    "the browser never talks to that channel" holds only for NERVE's server-side adapter,
    not for embedded sessions.
- **Permissions.** Per device group (mesh) and per device, as rights bitmasks (for
  example remote control 8, agent console 16, with restrictions such as no-terminal and
  no-files). Provision one MeshCentral account per operator through `adduser` and
  `addusertodevicegroup` / `adddeviceuser`, with explicit minimal rights, never the full
  admin mask.
- **Agents.** MeshAgent connects outbound to `wss://…/agent.ashx`. MeshCentral must be
  reachable by endpoints (`TlsOffload` and `certUrl` support a TLS-terminating reverse
  proxy). It can share an origin via proxy routes or path-based domains, but a separate
  origin is simpler and keeps NERVE's CSP (`frame-ancestors 'none'`) intact unless
  embedding is chosen deliberately.
- **Audit correlation.** `events` results are filtered by the caller's rights, and some
  actions are marked `nolog`. Polling events from a service account is useful but **not a
  complete audit**. NERVE must record intent itself before each handoff or command.
- Output limits for `runcommands` were not confirmed.

### Recommended first integration step

Run a time-boxed spike beside a disposable NERVE stack:

1. Read-only adapter: `nodes` and `getsysinfo` through a dedicated, least-privilege
   service account.
2. Operator-confirmed `meshcentral` device identities.
3. Presence and capabilities on the device page through the existing provider registry.
4. **Deep links** (new tab, separate origin) before any embedding. Every handoff is
   audited in NERVE.

Per-operator MeshCentral accounts and server-minted short-lived login cookies need a
decision on storing the login-token key. That needs encrypted integration credentials
with key rotation first.

## Security requirements still open before privileged commands

Native privileged commands are limited to a fixed allowlist (service restart/stop, process
termination, reboot/shutdown, log retrieval, agent update). `command.shell`,
`command.powershell` and `script.run` are not built natively; interactive shell and files
come from MeshCentral sessions (decision 0006). The allowlisted commands stay disabled
until all of these exist:

- HTTPS enforced for agents that may run privileged capabilities. Core-signed responses
  (implemented) give authenticity and integrity, not confidentiality.
- A per-command operator approval record that the agent can verify (today the agent trusts
  any command Core signs; a compromised Core can therefore still order read-only commands).
- Device trust: the target device must be `trusted`. The trust model exists; the policy
  check will be added together with the first privileged command.
- Explicit operator confirmation showing the target identity and evidence.
- Per-device or per-group authorization scope, plus policies or allowlists.
- A privileged helper separated from the unprivileged agent.
- Signed agent releases and updates.
