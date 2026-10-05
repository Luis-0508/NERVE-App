# Device identity

A NERVE device is the stable record that discovery, management agents and future
integrations (MeshCentral nodes, Proxmox guests, Docker hosts) refer to. This page
describes how NERVE decides what belongs to a device. It also explains how
operators own and correct that record.

## Discovered values and human-owned values

Every device keeps what discovery and agents report apart from what operators decide.
Core resolves the value shown to users.

| | Discovered (written by discovery and agents) | Human-owned (operators only) | Effective (shown) |
| --- | --- | --- | --- |
| Name | best hostname, else the first address (`discovered_name`) | manual name (`display_name`) | manual name, else discovered |
| Type | `classification` | `classification_override` (fixed vocabulary) | override, else discovered |
| Hostname, OS, vendor, model, serial | the most confident sourced fact | — | discovered |
| Trust state, tags, owner, location, notes | — | yes | as set |

Example: discovered hostname `DESKTOP-ABC123`, manual name `Luis-PC`, effective name
`Luis-PC`. A later scan keeps updating the discovered hostname, but the effective name
stays `Luis-PC` until an operator clears the manual name.

**Enforced by PostgreSQL:**
- A trigger rejects any change to human-owned fields or trust that does not carry an
  operator stamp (`metadata_updated_*`, `trust_changed_*`). Discovery and agents never
  write those stamps, so they cannot overwrite operator decisions, even through a bug.
- A second trigger keeps a manual name in `entities.name`, which other views (map,
  relationships, agents, events) read.

## Provenance

`device_attributes` records every descriptive claim with its source and confidence:

| Source | Attributes | Confidence |
| --- | --- | --- |
| `reverse-dns` (local discovery) | hostname | 50 |
| `nerve-agent` (enrolled management agent) | hostname, operating system, architecture | 85 |

The discovered value is the most confident current claim (then the most recent). The
device's Identity tab lists every claim and marks which one is used. When an agent is
detached or removed, its claims are deleted and the values are recomputed from what
remains.

## Trust state

Trust is operator-owned and audited (`device.trust.changed`, with an optional reason).

| State | Meaning |
| --- | --- |
| `unknown` | Default for every discovered device |
| `reviewed` | An operator looked at it |
| `trusted` | An operator vouches for it |
| `blocked` | Must not be interacted with; remote commands are refused |

Nothing changes trust automatically, and discovery never promotes a device. Merging two
devices never raises trust. See [Remote management](REMOTE_MANAGEMENT.md#device-trust)
for how trust gates remote actions.

## Identity evidence

`GET /api/v1/devices/{id}/identity` explains why NERVE believes a record is one device.
It returns the evidence with its strength, conflicts with other records, management
agents, sourced facts, and how discovery matched observations over the last 30 days.

| Evidence | Strength | Why |
| --- | --- | --- |
| Enrolled agent identity | strong | Ed25519 key unique to the endpoint |
| Agent machine ID digest | strong | Reported by the enrolled agent |
| Serial number | strong | Hardware identifier |
| Globally unique MAC | strong | Burned-in address |
| Locally administered MAC | moderate | May be randomized (phones, laptops, VMs) |
| Any evidence also claimed by another record | moderate | Conflict; see duplicates |
| Hostname | weak | Reused and renamed |
| IP address | weak | Changes and is reassigned |

A device is never identified by its primary address alone.

## How discovery attributes an observation

The rules live in `internal/discovery/match.go`. The rule that applied is recorded with
every observation:

1. **MAC identity.** If the MAC belongs to several records, the record with a live
   management agent wins, then the most recently seen one (`mac-ambiguous`). The
   conflict stays visible as evidence and as a duplicate candidate.
2. **Previous address binding.** Used unless the observation carries a MAC and the
   bound device already has a different MAC. In that case the address was reused by
   other hardware: the old device loses the address, and a new record starts
   (`address-reused-by-other-hardware`).
3. **Address reported by an enrolled agent.** Applies only to observations without a
   MAC (`agent-reported-address`). An IP change on an agent-managed device therefore
   does not create a duplicate.
4. **Otherwise a new device** (`new-device`).

Discovery never merges devices.

## Management agents and devices

Discovery and remote management converge on one device record; there is no separate RMM
identity.

- **Associating.** When an agent is associated (automatically through a unique MAC, or
  confirmed by an operator), its machine ID, MACs, hostname and addresses become
  identity evidence (source `nerve-agent`). Its hostname, OS and architecture become
  sourced facts.
- **Address changes.** The association is keyed to the device, not to an address, so it
  survives IP changes.
- **Operator decisions.** Confirmed and dismissed associations are never changed by
  discovery or heartbeats.
- **Ambiguity.** Ambiguous MAC matches remain candidates. See
  [Remote management](REMOTE_MANAGEMENT.md#heartbeat-capabilities-and-device-association).

## Duplicate detection

`GET /api/v1/devices/duplicates` (optionally `?deviceId=`) lists likely duplicate pairs
among live devices. It never changes anything.

| Shared evidence | Confidence |
| --- | --- |
| Machine ID, serial number or globally unique MAC | high |
| Locally administered MAC | medium |
| An address an enrolled agent reports, held by another record | medium |
| Hostname and a current address together | medium |
| Hostname alone or address alone | not reported |

Operators can mark a pair as different devices (`POST /api/v1/devices/duplicates/dismiss`).
The pair then stops being suggested.

## Merge

`POST /api/v1/devices/{target}/merge` with `{sourceDeviceId, reason}` needs
`devices.merge`. The operator chooses the surviving target. It runs as one transaction:

- **Child records move to the target:**
  - interfaces and addresses, discovery bindings, physical ports, hypervisor and
    container-host links
  - identity evidence (first and last seen are combined), sourced facts, tags
  - service endpoints (same protocol and port collapse into the target's), relationships
    (links between the two are dropped), observations, events, metrics
  - the management agent (keeping its association status) and enrollments
- **Human-owned metadata:** the target's values win, and blanks are filled from the
  source.
- **Trust:** `blocked` wins; otherwise the lower of the two states is kept.
- **The source is not deleted.** It stays as a tombstone with `merged_into`, its detail
  page points at the survivor, and lists hide it.
- **Records:** an append-only `device_merges` record snapshots the source's metadata. The
  merge is audited (`device.merged`) and recorded as an identity event. Remote command
  history of the source appears on the survivor.
- **Refused when:** both devices have a live management agent, either device is already
  merged, or source and target are the same.
- **Concurrency:**
  - Merges lock both devices in a fixed order.
  - Discovery holds a share lock on the device it writes to and follows `merged_into`.
    A merge therefore either waits for a running scan report and moves its new records,
    or finishes first, and the report lands on the survivor.
  - Triggers reject any new reference to a retired device and any attempt to restore one.

## Split

`POST /api/v1/devices/{id}/split` with `{displayName, interfaceIds, identityIds, agent, reason}`
needs `devices.merge`. It separates records that NERVE grouped wrongly.

- The operator chooses the interfaces and identities to move. An interface carries its
  addresses, discovery bindings and MAC evidence with it.
- If the device has a management agent, the operator must choose `keep`, `move` (the
  agent and its evidence go to the new device as a confirmed association) or `detach`.
- Observations and events stay on the original device, because they cannot be
  re-attributed reliably.
- The new device starts with trust `unknown`. An append-only `device_splits` record is
  written, and the split is audited (`device.split`).

## Audit

| Action | Recorded parameters |
| --- | --- |
| `device.metadata.changed` | changed fields with old and new values; notes only as changed and their length |
| `device.trust.changed` | from, to, reason |
| `device.merged` | merge ID, source, target, moved record counts, trust outcome, reason |
| `device.split` | split ID, new device, moved interfaces and identities, agent decision |
| `device.duplicate.dismissed` | both devices, reason |
| `agent.association.changed` / `agent.association.evaluated` | association decisions (see remote management) |

## Limitations

- No vendor (OUI) lookup yet. The vendor stays empty unless a source reports it.
- Classification is discovered only as `unknown` today, and operators set the type
  manually.
- Hostname and address evidence come from reverse DNS and agents only. There are no DHCP,
  SNMP, LLDP or ARP-history sources yet.
- Duplicate candidates are computed on request. There is no background job or
  notification.
- A split cannot move individual observations or events, and the UI splits by interface
  only (the API also accepts identity IDs).
- Merges cannot be undone; a mistaken merge is corrected with a split.
- `devices.approved_at` from the original schema is no longer used. Trust state replaces
  it.
