# 0008. Windows channel for the privileged helper

- Status: Accepted
- Date: 2026-11-03

## Context

Decision 0007 defines the privileged helper and implements it on Linux with a Unix socket
and `SO_PEERCRED`. Windows needs the same guarantees: only the agent's virtual account
(`NT SERVICE\NERVEAgent`) may call the helper, the agent talks only to the real helper, and
the helper trusts only state that administrators control. The agent account is not an
administrator and cannot open the token of a SYSTEM process, so it cannot identify the
helper by its process the way the Linux agent checks the socket's peer.

## Decision

- **Service.** `NERVEAgentHelper` runs `nerve-agent.exe helper` as LocalSystem. The installer
  refuses `--with-helper` unless the binary resolves to a path under `%ProgramFiles%`,
  because SYSTEM runs it.
- **Channel.** The named pipe `\\.\pipe\NERVEAgentHelper`, created with an explicit owner
  (SYSTEM), a protected DACL that gives SYSTEM full access and the agent's service SID only
  read and write (no right to create pipe instances), `FILE_FLAG_FIRST_PIPE_INSTANCE` and
  `PIPE_REJECT_REMOTE_CLIENTS`. Overlapped I/O is implemented on `golang.org/x/sys/windows`
  without a new dependency.
- **Helper checks the caller** by the token user of the client process
  (`GetNamedPipeClientProcessId`): the agent's service SID or SYSTEM.
- **Agent checks the helper** by the owner of the pipe: it must be SYSTEM. All instances of a
  pipe share the first instance's security descriptor, and a non-administrator cannot
  create an object owned by SYSTEM. The agent opens the pipe with
  `SECURITY_IDENTIFICATION`, so the helper cannot act as the agent.
- **State.** `%ProgramData%\NERVE\helper` (`helper.json`, `executed.json`) has a protected
  DACL for SYSTEM and Administrators. The helper refuses state whose owner is not SYSTEM or
  Administrators, whose DACL grants any write right to another SID, or that is a reparse
  point. The same check covers the parent `%ProgramData%\NERVE`, which the installer gives a
  protected DACL (others may only read it), because standard users may create folders in
  `%ProgramData%` and an owner of the parent could rename the helper directory. An installed
  helper whose pin is missing is re-pinned only by a new enrollment.

## Alternatives considered

- **go-winio for named pipes:** maintained and widely used, but a new dependency for about
  200 lines of pipe I/O. Rejected for now; worth revisiting if pipe handling grows.
- **Agent checks the helper's process token:** fails in production because the virtual
  account cannot query a SYSTEM token.
- **Loopback TCP with a shared secret:** a secret the helper would have to protect from the
  agent account, and a network listener for a SYSTEM process. Rejected.

## Consequences

- Windows has no equivalent of `PrivateNetwork=true` for a service, so the helper is not
  network-isolated there; it still never opens a connection itself.
- The pipe implementation needs native Windows tests; they run in the agent's test binary
  on Windows without administrator rights.
