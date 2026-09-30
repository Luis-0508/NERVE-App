# Traffic Map: 3D topology view

The Traffic Map opens in **Topology 3D** by default. **World** keeps the original
geographic map. Both views share the same live traffic snapshot, filters, flow list,
flow inspector and diagnostics. The last chosen view is remembered in the browser.

## Data sources

| Element | Source | Provenance |
| --- | --- | --- |
| Devices | `GET /api/v1/devices` (up to 1,000, refreshed every 30 s) | Inventory |
| Hosts marked "Not in inventory" | `localIp` of live flows without a matching device `primaryIpAddress` | Observed traffic |
| Remote endpoints | `remoteIp` of live flows | Observed traffic |
| Links and pulses | Live flow groups, aggregated per local host, remote IP and direction | Measured |
| Plates | Derived /24 address block of each host (IPv6 and hosts without an address are grouped separately) | Derived, not a verified VLAN or segment |
| Globe pins | Approximate DB-IP location of each remote IP, placed at its exact latitude/longitude | Approximate |
| Uplink | The local site set in the Traffic Map (browser-only), or a floating uplink without location | User set |

NERVE does not yet record links between local devices, gateways or switches, so the view
draws none. Devices classified as routers, switches or servers are placed at the back of
their plate. They are not wired to other devices.

## Visual encoding

- Only measured traffic emits light. Devices, plates and the globe are matte.
- Cable brightness and pulse density scale with measured throughput. Pulse speed is
  illustrative. Outbound uses the accent colour and inbound uses the info colour,
  matching the World view.
- The status ring under each device uses the NERVE status colours. Offline devices are
  dimmed. Warning and starting rings pulse gently.
- A globe floats above the network. It uses the same Natural Earth data as the World view,
  so pins, land and the World map all show one geography. Pins that share a location stack
  outward instead of moving.
- Each link runs from its device up to the uplink, then along a great-circle arc over the
  globe to its pin. When a local site is set, the uplink is that site on the globe, and the
  globe turns so the site faces the network. Without a local site, the uplink floats below
  the globe and no location is implied.
- Endpoints without a location sit in a separate "Location unknown" cluster beside the globe.

## Interaction

Drag to orbit, right-drag or use the arrow keys to pan, and scroll to zoom. Select a
device or pin to highlight its flows and focus the camera. Selecting a pin flies the camera
around the globe to it. Labels and pins on the far side of the globe are hidden. The inspector lists its
flow groups, and it links to the device page. Escape or a click on empty space clears the
selection. Use the toolbar to fit the topology to the view or to toggle device labels.
Labels are decluttered by priority.

## Performance

The three.js chunk loads only when the 3D view is shown. Devices and endpoints are
instanced meshes. Pulses are animated on the GPU and read each route from a small float texture. The
scene renders on demand, runs ambient animation at about 30 fps, and stops while the
map is off-screen, the tab is hidden or the map is paused. Reduced-motion users get
static pulses and instant camera moves.
