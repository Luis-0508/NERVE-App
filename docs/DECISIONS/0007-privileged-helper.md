# 0007. Privileged helper with an install-time trust anchor

- Status: Accepted
- Date: 2026-11-03

## Context

The agent runs unprivileged (a Windows virtual account, the `nerve-agent` systemd user).
Decision 0006 limits native privileged work to a fixed allowlist (service restart/stop,
process termination, reboot/shutdown, log retrieval, applying agent updates). Those actions
need administrator rights, and the approval gate (#99) already gives every privileged
command a Core-signed, short-lived approval bound to one command, agent and parameter set.

The threat to design against: an attacker who controls the agent account (a bug in the
agent, a stolen identity file) must not be able to run privileged code beyond what an
operator approved. Everything the agent account can write is therefore untrusted for the
privileged side, including its identity file and the Core key pinned in it.

## Decision

- **Separate process.** Privileged work runs in `nerve-agent helper`, installed as a second
  service with `service install --with-helper` (a root systemd unit; LocalSystem on
  Windows in a follow-up). The default installation has no helper.
- **No network.** The helper never talks to Core. The unit runs with `PrivateNetwork=true`
  and `RestrictAddressFamilies=AF_UNIX`; it only answers the local agent.
- **Install-time trust anchor.** The installer, running as root, copies the agent ID and
  the pinned Core key from the enrolled identity into `helper.json` in the helper's
  root-only state directory. The helper verifies approvals only against that file, never
  against anything the agent account can change. The file holds a list of keys so Core key
  rotation (#98) can add keys later.
- **Local channel.** A Unix socket in a root-owned runtime directory, group `nerve-agent`,
  mode `0660`. The helper checks the caller with `SO_PEERCRED` (the agent user or root);
  the agent checks that the socket is served by root. One JSON request per connection.
- **Verification in the helper.** For every request: valid Core signature with a key from
  `helper.json`, approval and command bound to the pinned agent, matching type and
  parameter digest, unexpired approval and command deadline, type in the helper's compiled
  allowlist, and a command ID never seen before. The replay register is written and synced
  to disk before the handler runs, and entries are kept beyond the approval's lifetime.
- **Device binding** is covered by the signature: the approval names the device, and only
  Core can sign. Pinning the device would break on device merges and splits.
- **Capabilities.** The agent advertises a privileged capability only while the helper
  answers a health check, reports the same agent ID and lists the type. Without a helper,
  privileged commands fail closed with `helper_unavailable`.

## Alternatives considered

- **Helper reads the agent's identity file:** no extra install step, but the agent account
  could swap the pinned Core key and sign its own approvals. Rejected.
- **Helper with its own Core connection and identity:** independent verification of the
  command queue, but a second enrolled identity, network access for a root process and
  more attack surface. Rejected.
- **Signed health check instead of peer credentials:** the helper would need its own key,
  stored where the agent cannot read it, only to prove what the kernel already proves
  through `SO_PEERCRED`. Rejected.
- **Running privileged actions through MeshCentral:** see decision 0006.

## Consequences

- Re-enrolling the agent or rotating the Core key needs `service install --with-helper`
  again so the helper trusts the new identity; until then the helper refuses and the agent
  stops advertising privileged capabilities. #98 should add a signed key hand-over.
- The allowlist is compiled into the helper. In this change it is empty in production
  builds; #92 adds the real command types and the systemd capabilities each one needs.
- Windows needs its own channel (a named pipe restricted to `NT SERVICE\NERVEAgent`) and
  service registration; until then `--with-helper` is refused on Windows.
