# NERVE agent setup

`nerve-agent` is one binary with two roles:

- **Management agent**: enrolled per host with its own Ed25519 identity. It reports
  heartbeats, its run state and capabilities, and runs read-only remote-management
  commands. See [Remote management](REMOTE_MANAGEMENT.md).
- **Discovery worker** (legacy): scans the configured subnet using the shared local
  `NERVE_DISCOVERY_AGENT_TOKEN`. On Windows, `start.bat` runs it automatically.

The process runs whichever roles are configured: management when an identity exists,
discovery when the discovery token is set. It exits if neither is configured.

For permanent operation, install the management agent as a **service** (Windows service or
systemd unit). The agent only makes outbound HTTP(S) connections to Core; managed devices
need no inbound ports.

## Build

```sh
# Windows: nerve-agent.exe, install-windows.ps1, uninstall-windows.ps1
docker build --file Dockerfile.agent --target artifact --build-arg TARGETOS=windows --build-arg NERVE_VERSION=1.0.0 --output .nerve/agent-windows .
# Linux: nerve-agent, install-linux.sh, uninstall-linux.sh
docker build --file Dockerfile.agent --target artifact --build-arg TARGETOS=linux --build-arg NERVE_VERSION=1.0.0 --output .nerve/agent-linux .
```

`NERVE_VERSION` is the version the agent reports to Core (`dev` by default). Signed
release artifacts are not published yet; build from a reviewed checkout.

## Install as a service

1. In NERVE, open **Settings → Management agents → Enroll an agent** (requires
   `agents.manage`), optionally name the agent and create a token. It is shown once and
   expires after the chosen time (5 minutes to 24 hours). To link the agent to a known
   device immediately, use `POST /api/v1/agent-enrollments` with `deviceId`.
   For many endpoints, create a **deployment key** under **Deployment keys** instead and
   use it in place of the token on every endpoint. With the default "Wait for approval",
   each agent then appears as *Awaiting approval*; compare its host and key fingerprint
   and choose **Approve** or **Reject**. `nerve-agent status` and `diagnose` report the
   pending state on the endpoint.
2. Copy the build artifact to the endpoint and save the token to a file that only
   administrators can read. Tokens are never accepted as command-line arguments, because
   other local users can read process arguments.
3. Run the installer with the Core API URL (the same origin as the web UI, plus
   `/api/v1`):

   ```powershell
   # Windows, elevated PowerShell, in the artifact folder
   .\install-windows.ps1 -ApiUrl https://nerve.example.lan/api/v1 -TokenFile .\token
   ```

   ```sh
   # Linux with systemd
   sudo ./install-linux.sh --api-url https://nerve.example.lan/api/v1 --token-file ./token
   ```

   On Windows, **Copy PowerShell command** next to the token does steps 2 and 3 in one
   line without a token file: it sets `NERVE_AGENT_ENROLLMENT_TOKEN` for the session, runs
   `install-windows.ps1` (or `nerve-agent enroll --force` for a re-enrollment token) and
   clears the variable again. Both `service install` and `enroll` read the token from that
   variable when no `--token-file` is given. The pasted line can stay in the PowerShell
   history; the token is single-use and expires, so revoke it if enrollment does not finish.

   The installer prints the agent ID and key fingerprint. Compare the fingerprint with the
   one shown in NERVE, then delete the token file. The token carries the fingerprint of
   NERVE Core's signing key, so the agent refuses to enroll with a server that cannot prove
   it holds that key, even over plain HTTP.
4. The agent appears in **Settings → Management agents** as *Online* with its run mode
   (*Windows service* or *systemd service*).

Add `-CaFile` / `--ca-file <pem>` when Core's HTTPS certificate is issued by a private CA.
The installer copies it to `ca.pem` in the state directory, where the service account can
read it; pass the option again to replace the copy. The installers are idempotent: running them again with a new binary and no token upgrades
the agent and keeps its identity and configuration.

| | Windows | Linux |
| --- | --- | --- |
| Binary | `%ProgramFiles%\NERVE\Agent\nerve-agent.exe` (writable only by administrators) | `/usr/local/bin/nerve-agent` (root-owned, `0755`) |
| Service | `NERVEAgent`, automatic (delayed) start | `nerve-agent.service`, enabled |
| Account | Virtual account `NT SERVICE\NERVEAgent` (no password, not an administrator) | System user `nerve-agent` (no login shell, no capabilities) |
| State | `%ProgramData%\NERVE\agent` (protected DACL: SYSTEM, Administrators, the service account) | `/var/lib/nerve-agent` (`0700`, owned by `nerve-agent`) |
| Logs | `%ProgramData%\NERVE\agent\logs\agent.log` (JSON, up to 4 files of 10 MiB) plus start, stop and failure events in the Windows Application log (source `NERVEAgent`) | journald: `journalctl -u nerve-agent` (JSON lines) |
| Restart | Recovery actions: restart after 5 s, 10 s, then every 60 s, also after non-crash failures | `Restart=always`, `RestartSec=5`, no start limit, systemd watchdog (`WatchdogSec=180`) |
| Hardening | Per-service SID, no administrative rights | `NoNewPrivileges`, empty capability set, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, kernel and cgroup protection, `MemoryDenyWriteExecute`, `UMask=0077` |

The service runs with minimal rights on purpose. The read-only collectors work without
administrative rights; some details (for example the memory use of other users'
processes) may be missing. Privileged actions will use a separate, explicitly controlled
helper, never a privileged main agent.

The installers wrap `nerve-agent service …`, which you can also call directly:

```text
nerve-agent service install [--api-url URL] [--token-file FILE] [--ca-file FILE] [--force] [--no-start]
nerve-agent service uninstall [--purge]
nerve-agent service start | stop | restart | status
```

`service install` refuses a Linux binary that a non-root account could replace and warns on
Windows when the binary is outside `%ProgramFiles%`. A Windows MSI package is not available
yet; the PowerShell installer is the supported path.

### Uninstall

```powershell
.\uninstall-windows.ps1          # keeps the identity for a reinstall
.\uninstall-windows.ps1 -Purge   # also deletes identity, configuration and logs
```

```sh
sudo ./uninstall-linux.sh [--purge]
```

Also **revoke** or **remove** the agent in NERVE. Deleting the local identity destroys the
private key but does not tell Core.

## Lifecycle and reliability

- **Start-up.** The service reads `config.json` and `identity.json` from its state
  directory. A missing or unreadable identity stops the service with an error, and the
  restart policy retries.
- **Heartbeat.** Every 30 seconds, with host facts, capabilities, agent version, run mode,
  process start time and host boot time. An agent is *online* when Core saw an
  authenticated request in the last 90 seconds.
- **Graceful stop** (service stop, shutdown, reboot). The agent stops claiming work,
  reports an interrupted command as failed (`agent_stopped`) and sends a final "stopping"
  heartbeat. NERVE then shows it as **Stopped** immediately instead of waiting 90 seconds.
  A stop is bounded to 15 seconds, so it never holds up a shutdown.
- **Crashes.** The service manager restarts the process. Core's timeout sweeper fails
  commands that were running; Core never hands a command out twice.
- **Network loss and Core restarts.** Requests retry with jittered exponential backoff
  (2 s to 60 s). A silent agent is shown as **Offline** and reconnects on its own.
- **Re-enrollment while running.** `nerve-agent enroll --force` (as Administrator or root)
  replaces the identity. The running service notices the new credential within 15 seconds
  and switches over without a restart. This also recovers a revoked agent.

## Configuration

`service install` writes `config.json` in the state directory. Environment variables
override it (useful for foreground runs).

| Key | Environment | Meaning |
| --- | --- | --- |
| `apiUrl` | `NERVE_AGENT_API_URL` | Core API base URL ending in `/api/v1`. Falls back to the URL recorded at enrollment. |
| `caFile` | `NERVE_AGENT_CA_FILE` | Additional PEM CA trusted for Core's certificate (system roots stay trusted). |
| `disabledCapabilities` | `NERVE_AGENT_DISABLED_CAPABILITIES` (comma-separated, added to the file's list) | Capabilities this host never advertises or runs. |
| `logLevel` | `NERVE_AGENT_LOG_LEVEL` | `debug`, `info` (default), `warn` or `error`. |
| `credentialMaxAgeDays` | — | Rotate the agent key automatically once it is older than this many days (checked hourly). `0` or absent turns it off. |

Unknown keys are rejected. On Linux the agent refuses a configuration file that other
accounts can modify. TLS certificate verification is always on (TLS 1.2 minimum) and
cannot be disabled; use `caFile` for a private PKI.

## Credential storage

The identity file `identity.json` holds the agent ID, credential ID, Core URL, the
Ed25519 private key seed and the pinned NERVE Core public key. It is written atomically.

| Mode | Default directory | Protection |
| --- | --- | --- |
| Windows service | `%ProgramData%\NERVE\agent` | Protected DACL: SYSTEM, Administrators and `NT SERVICE\NERVEAgent` |
| Windows, foreground | `%LOCALAPPDATA%\NERVE\agent` of the account that runs the agent | Protected DACL: SYSTEM, Administrators and that account; inheritance removed |
| Linux service or root | `/var/lib/nerve-agent` | Directory `0700`, files `0600`; the agent refuses an identity file that other users can read. Files written by root are handed to the service account. |
| Linux, user | `$XDG_STATE_HOME/nerve-agent` or `~/.local/state/nerve-agent` | Same |

When a service is installed, elevated administrators and root automatically use the
machine directory, so `nerve-agent status`, `diagnose` and `enroll --force` act on the
service's identity. `NERVE_AGENT_STATE_DIR` or `--state-dir` overrides the location.
Anyone who can read the identity file can act as that agent, so treat it like an SSH host
key. If the file is exposed, revoke the agent in NERVE and re-enroll.

## Diagnostics

```sh
nerve-agent diagnose          # human-readable
nerve-agent diagnose --json   # machine-readable, for support tickets
```

`diagnose` checks, in order: service state, identity, configuration, DNS, TCP, TLS
(certificate chain, issuer and expiry; plain HTTP is a warning), clock offset to Core, and
a signed identity check whose response must carry the pinned Core signature. It never
prints keys or tokens. `nerve-agent status` prints the identity and verifies it with Core.

## Run in the foreground

Without a service, `nerve-agent` (or `nerve-agent run`) runs until interrupted and logs JSON
to stdout. Enroll first with `nerve-agent enroll --api-url … --token-file …`, or set
`NERVE_AGENT_ENROLLMENT_TOKEN` to enroll automatically on first start. On the NERVE host,
`start.bat` already runs the agent for discovery. If that account has enrolled once
(identity in its `%LOCALAPPDATA%`), the same process also runs management.

## Capabilities

| Capability | Windows | Linux |
| --- | --- | --- |
| `system.info` | ✓ | ✓ |
| `system.metrics` (CPU sample, memory, fixed disks) | ✓ | ✓ |
| `process.list` (pid, parent, name, threads, memory, user on Linux; no command lines) | ✓ | ✓ |
| `service.list` | Service Control Manager | systemd only |
| `software.list` | Uninstall registry keys (machine and user) | dpkg or rpm |

Use `disabledCapabilities` (or `NERVE_AGENT_DISABLED_CAPABILITIES`) to stop advertising and
running specific capabilities on a host.

## Revoke, re-enroll, remove

- **Revoke** (Settings → Revoke): the key stops working immediately and pending commands
  are cancelled. The agent logs `re-enroll this agent` and pauses.
- **Re-enroll** (Settings → Re-enroll): creates a token bound to the same agent ID. Run
  `nerve-agent enroll --force --token-file …` as Administrator or root. A running service
  picks up the new key within 15 seconds; a foreground agent does too. The agent keeps its
  ID and history.
- **Rotate the key**: `nerve-agent rotate-key` (as Administrator or root for an installed
  service) replaces the key without an operator token; the old key is revoked as
  `rotated`. A running service picks up the new key within 15 seconds. `nerve-agent status`
  shows the key age and an interrupted rotation. See
  [Key rotation](REMOTE_MANAGEMENT.md#key-rotation).
- **Remove**: retires the agent permanently. To manage the host again, enroll it as a
  new agent.

## Troubleshooting

Start with `nerve-agent diagnose`.

| Agent log / error | Meaning |
| --- | --- |
| `agent_clock_skew` | The host clock differs from Core by more than 2 minutes. Fix NTP. |
| `agent_credential_revoked` | The agent was revoked or removed. Re-enroll it or retire it. |
| `agent_authentication_failed` | Unknown credential or wrong key, for example an identity file copied from another host. Re-enroll. |
| `enrollment_rejected` | The token or deployment key is invalid, expired, revoked, already used or out of uses. For a deployment key with the correct secret, the message names the reason, for example "has reached its use limit (25 of 25)". Create a new one. |
| `not signed by the pinned NERVE Core key` | A response could not be verified: something between the agent and Core altered or forged it, or Core's signing key changed. The agent ignores it and retries. If Core's key was changed on purpose, re-enroll the agent. |
| `not signed by the pinned NERVE Core key` together with `HTTP 503` | Usually Core has no `NERVE_CORE_SIGNING_KEY`: that 503 answer cannot be signed. Check the API logs, run `setup.bat` (or set a 64-hex seed in `.env`) and recreate the API container. |
| `core_signing_key_missing` (HTTP 503) | Core has no `NERVE_CORE_SIGNING_KEY`. Run `setup.bat` (or set a 64-hex seed in `.env`) and recreate the API container. |
| `no pinned NERVE Core key` | The identity file predates Core authentication. Re-enroll the agent. |
| `… is accessible by other users` / `… is writable by other users` | The state file permissions are too open. Run `chmod 600` on it. |
| `certificate not trusted` in `diagnose` | Core's certificate is not trusted. Install it with a public CA, or pass the private CA with `--ca-file`. |
| `command claim failed … HTTP 502` | Core is restarting. The agent retries with backoff. |
| Windows service stops right after starting | See `%ProgramData%\NERVE\agent\logs\agent.log` and the Application event log (source `NERVEAgent`). Usually the host is not enrolled yet. |
| `nerve-agent status` says "not enrolled" on Windows | Run it from an elevated prompt; the service identity is readable only by administrators. |
