# Third-party notices

Data bundled into NERVE builds that comes from outside the project. Code dependencies are
listed in `go.mod` and `package.json` with their own licences.

## IEEE Registration Authority MAC address block listings

- Used for: vendor lookup of observed MAC addresses (`internal/oui/registry.tsv`, embedded
  in the Core server).
- Source: IEEE Registration Authority public listings MA-L, MA-M and MA-S,
  <https://standards-oui.ieee.org/>.
- Content: assignment prefix and organization name only. Organization addresses, private
  assignments and registry-held blocks are removed by `scripts/generate-oui.mjs`.
- Terms: published by the IEEE for public lookup; the files carry no licence statement. See
  [decision 0004](docs/DECISIONS/0004-ieee-oui-registry.md).

## Maps and geolocation

Country boundaries (world-atlas, Natural Earth) and DB-IP geolocation databases: see
[public/maps/NOTICE.md](public/maps/NOTICE.md) and
[public/maps/DB-IP-NOTICE.md](public/maps/DB-IP-NOTICE.md).
