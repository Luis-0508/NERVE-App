# 0004. Embedded IEEE registry for MAC vendor lookup

- Status: Accepted
- Date: 2026-10-23

## Context

Most discovered devices showed no vendor, because only enrolled agents reported facts about
hardware. The first three octets of a universally administered MAC address name the
organization the IEEE assigned that block to. NERVE needs this lookup offline: observed MAC
addresses must never leave the installation, and Core makes no outbound requests on its own.
The repository has no licence file yet, so bundled third-party data must not constrain the
licence the project picks later.

## Decision

- The table is generated from the IEEE Registration Authority public listings: MA-L (24-bit),
  MA-M (28-bit) and MA-S (36-bit), from `standards-oui.ieee.org`.
- `scripts/generate-oui.mjs` downloads and normalises them into `internal/oui/registry.tsv`:
  hex prefix and organization name only, sorted. It drops the organization address, private
  assignments and the blocks listed as "IEEE Registration Authority" (blocks the IEEE
  subdivides), so a lookup in an unassigned sub-block finds nothing instead of naming the
  registry. Updating is a manual, reviewed pull request.
- `internal/oui` embeds the file with `go:embed` and does longest-prefix matching (MA-S, then
  MA-M, then MA-L). Multicast and locally administered addresses (which include randomized
  phone and laptop MACs) never resolve.
- The result is a sourced fact: attribute `manufacturer`, source `oui`, confidence 40. Every
  other source outranks it, because the vendor of a network adapter (virtual, USB or
  replacement) is not always the maker of the device.
- `THIRD_PARTY_NOTICES.md` records where the data comes from.

## Alternatives considered

- Wireshark's `manuf`: richer short names, but distributed under GPL-2.0, which would decide
  the licence question for the repository.
- Runtime download or an online lookup API: sends observed MAC addresses or at least network
  activity to a third party, and fails in isolated networks.
- No lookup: the vendor column stays empty for nearly every discovered device.

## Consequences

- No network access at runtime; the server binary grows by about 1.6 MB. The agent binary
  does not include the table, because nothing it links calls the lookup.
- Organization names are the IEEE's legal names (for example "Apple, Inc.", "Microsoft
  Corporation"), not short brand names.
- The table ages. A device whose block was assigned after the last regeneration shows no
  vendor until someone reruns the generator.
- The IEEE publishes the listings openly for lookup and attaches no licence terms to the CSV
  files. The table holds only factual assignment data. If the IEEE ever restricts
  redistribution, the generator and the embedded file are the only things to replace.
