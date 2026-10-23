import type { DeviceStatus, DeviceSummary, TrafficDirection, TrafficFlow } from "@/lib/types";

// Deterministic 3D topology derived only from observed NERVE data. Plates group devices by
// address block (derived, not a verified segment); a globe above the network pins remote
// endpoints at their approximate IP location. Nothing here invents links: every link is a
// measured flow, routed through the local site's uplink.

export type Vec3 = [number, number, number];
export type NodeRole = "gateway" | "switch" | "server" | "client" | "unknown";
export type TopologyNode = {
  id: string;
  kind: "device" | "observed" | "remote";
  role: NodeRole;
  label: string;
  ip?: string;
  status: DeviceStatus;
  device?: DeviceSummary;
  groupId?: string;
  located?: boolean;
  lat?: number;
  lon?: number;
  countryCode?: string;
  countryName?: string;
  city?: string;
  position: Vec3;
  bytesPerSecond: number;
  flowIds: string[];
};
export type TopologyLink = { id: string; source: string; target: string; direction: TrafficDirection; bytesPerSecond: number; flowIds: string[]; applications: string[] };
export type TopologyGroup = { id: string; label: string; center: [number, number]; size: [number, number]; count: number };
export type GlobeLabel = { code: string; name: string; position: Vec3; normal: Vec3; bytesPerSecond: number };
export type GeoPoint = { lat: number; lon: number };
export type Globe = { radius: number; center: Vec3; alpha: number; gamma: number; site: GeoPoint | null; uplink: Vec3; unlocated: Vec3 };
export type Topology = {
  nodes: TopologyNode[];
  links: TopologyLink[];
  groups: TopologyGroup[];
  globe: Globe;
  countries: GlobeLabel[];
  lanRadius: number;
  focus: Vec3;
  extent: number;
};

export const roleOrder: NodeRole[] = ["gateway", "switch", "server", "client", "unknown"];
export const roleLabels: Record<NodeRole, string> = { gateway: "Router / gateway", switch: "Switch / access point", server: "Server / storage", client: "Client", unknown: "Unclassified" };

const rolePatterns: [NodeRole, RegExp][] = [
  ["gateway", /router|gateway|firewall|modem|\bfw\b/],
  ["switch", /switch|access.?point|\bap\b|bridge|mesh/],
  ["server", /server|nas|storage|hypervisor|virtual|\bvm\b|container|cluster/],
  ["client", /workstation|desktop|laptop|notebook|\bpc\b|computer|phone|mobile|tablet|printer|tv|camera|iot|console|media|speaker|client/],
];
export function roleFor(classification: string | undefined): NodeRole {
  const value = (classification ?? "").toLowerCase();
  return rolePatterns.find(([, pattern]) => pattern.test(value))?.[0] ?? "unknown";
}

const SPACING = 3.3, PLATE_PADDING = 1.6, PLATE_GAP = 3.2;

function ipv4Parts(ip: string | undefined) {
  const parts = ip?.split(".").map(Number);
  return parts?.length === 4 && parts.every(p => Number.isInteger(p) && p >= 0 && p <= 255) ? parts : null;
}
export function addressBlock(ip: string | undefined): { id: string; label: string; sort: string } {
  const v4 = ipv4Parts(ip);
  if (v4) return { id: `v4:${v4.slice(0, 3).join(".")}`, label: `${v4.slice(0, 3).join(".")}.0/24`, sort: `0${v4.slice(0, 3).map(p => String(p).padStart(3, "0")).join(".")}` };
  if (ip?.includes(":")) return { id: "v6", label: "IPv6 hosts", sort: "1" };
  return { id: "none", label: "No IP address", sort: "2" };
}
function ipSortKey(ip: string | undefined) {
  const v4 = ipv4Parts(ip);
  return v4 ? v4.map(p => String(p).padStart(3, "0")).join(".") : `~${ip ?? ""}`;
}
// FNV-1a keeps per-endpoint jitter stable across renders and sessions.
export function hash(value: string) {
  let h = 0x811c9dc5;
  for (let i = 0; i < value.length; i++) { h ^= value.charCodeAt(i); h = Math.imul(h, 0x01000193); }
  return (h >>> 0) / 0xffffffff;
}

type Options = { devices: DeviceSummary[]; flows: TrafficFlow[]; deviceFilter?: "all" | "traffic" | "online"; site?: GeoPoint | null };

// When several inventory devices report the same address, traffic to that address belongs to
// the online, most recently seen one; the others stay visible without it. The rule applies per
// address, so a device can win one of its addresses and lose another.
function preferred(a: DeviceSummary, b: DeviceSummary) {
  return Number(a.status === "online") - Number(b.status === "online") || (Date.parse(a.lastSeenAt) || 0) - (Date.parse(b.lastSeenAt) || 0) || b.id.localeCompare(a.id);
}

// Current addresses only (the API omits ended leases), so an old lease never attracts traffic.
function currentAddresses(device: DeviceSummary) {
  return device.ipAddresses?.length ? device.ipAddresses : device.primaryIpAddress ? [device.primaryIpAddress] : [];
}

export function buildTopology({ devices, flows, deviceFilter = "all", site = null }: Options): Topology {
  const localByIP = new Map<string, TopologyNode>();
  const local: TopologyNode[] = [];
  const trafficIPs = new Set(flows.map(f => f.localIp));
  const inventoryIPs = new Set(devices.flatMap(currentAddresses));
  for (const device of devices) {
    const addresses = currentAddresses(device);
    if (deviceFilter === "online" && device.status !== "online") continue;
    if (deviceFilter === "traffic" && !addresses.some(ip => trafficIPs.has(ip))) continue;
    const node: TopologyNode = { id: `device:${device.id}`, kind: "device", role: roleFor(device.classification), label: device.name, ip: device.primaryIpAddress, status: device.status, device, position: [0, 0, 0], bytesPerSecond: 0, flowIds: [] };
    local.push(node);
    for (const ip of addresses) {
      const holder = localByIP.get(ip);
      if (!holder || preferred(device, holder.device!) > 0) localByIP.set(ip, node);
    }
  }
  // Hosts seen in traffic without an inventory device at that address stay visible as
  // observed-only. Addresses of inventory devices hidden by the device filter are not relabelled.
  for (const ip of [...trafficIPs].sort()) {
    if (localByIP.has(ip) || inventoryIPs.has(ip)) continue;
    const node: TopologyNode = { id: `host:${ip}`, kind: "observed", role: "unknown", label: ip, ip, status: "unknown", position: [0, 0, 0], bytesPerSecond: 0, flowIds: [] };
    local.push(node); localByIP.set(ip, node);
  }

  const groups = layoutGroups(local);
  const lanRadius = Math.max(8, ...groups.map(g => Math.hypot(Math.abs(g.center[0]) + g.size[0] / 2, Math.abs(g.center[1]) + g.size[1] / 2)));
  const globe = createGlobe(lanRadius, site);

  const remotes = new Map<string, TopologyNode>();
  const links = new Map<string, TopologyLink>();
  const countryTotals = new Map<string, { name: string; bytes: number; x: number; y: number; z: number; n: number }>();
  for (const flow of flows) {
    const source = localByIP.get(flow.localIp);
    if (!source) continue;
    let remote = remotes.get(flow.remoteIp);
    if (!remote) {
      const located = flow.remoteLatitude !== undefined && flow.remoteLongitude !== undefined && Number.isFinite(flow.remoteLatitude) && Number.isFinite(flow.remoteLongitude);
      remote = { id: `remote:${flow.remoteIp}`, kind: "remote", role: "unknown", label: flow.remoteIp, ip: flow.remoteIp, status: "online", located, countryCode: flow.remoteCountryCode, countryName: flow.remoteCountryName, city: flow.remoteCity, ...(located ? { lat: flow.remoteLatitude, lon: flow.remoteLongitude } : {}), position: [0, 0, 0], bytesPerSecond: 0, flowIds: [] };
      remotes.set(flow.remoteIp, remote);
    }
    source.bytesPerSecond += flow.bytesPerSecond; source.flowIds.push(flow.id);
    remote.bytesPerSecond += flow.bytesPerSecond; remote.flowIds.push(flow.id);
    const key = `${source.id}|${remote.id}|${flow.direction}`;
    const link = links.get(key) ?? { id: key, source: source.id, target: remote.id, direction: flow.direction, bytesPerSecond: 0, flowIds: [], applications: [] };
    link.bytesPerSecond += flow.bytesPerSecond; link.flowIds.push(flow.id);
    if (!link.applications.includes(flow.application)) link.applications.push(flow.application);
    links.set(key, link);
  }
  const remoteNodes = [...remotes.values()].sort((a, b) => a.id.localeCompare(b.id));
  // Pins sit exactly on their location; endpoints sharing a location stack outward instead of shifting.
  // Beyond PIN_STACK_MAX, further columns spiral tightly around the spot so busy CDN cities stay bounded.
  const stacks = new Map<string, number>();
  for (const remote of remoteNodes) {
    if (!remote.located) { remote.position = unlocatedPoint(remote.ip!, globe); continue; }
    const key = `${remote.lat!.toFixed(3)},${remote.lon!.toFixed(3)}`, index = stacks.get(key) ?? 0;
    stacks.set(key, index + 1);
    const column = Math.floor(index / PIN_STACK_MAX), level = index % PIN_STACK_MAX;
    const [lat, lon] = column ? spiralOffset(remote.lat!, remote.lon!, column, globe.radius) : [remote.lat!, remote.lon!];
    remote.position = globePoint(globe, lat, lon, PIN_HEIGHT + level * PIN_STACK);
    if (!remote.countryCode) continue;
    const direction = geoDirection(remote.lat!, remote.lon!), total = countryTotals.get(remote.countryCode) ?? { name: remote.countryName ?? remote.countryCode, bytes: 0, x: 0, y: 0, z: 0, n: 0 };
    total.bytes += remote.bytesPerSecond; total.x += direction[0]; total.y += direction[1]; total.z += direction[2]; total.n++;
    countryTotals.set(remote.countryCode, total);
  }
  const countries = [...countryTotals].map(([code, t]) => {
    const normal = rotate(normalize([t.x, t.y, t.z]), globe.alpha, globe.gamma);
    return { code, name: t.name, bytesPerSecond: t.bytes, normal, position: add(globe.center, normal, globe.radius * 1.22) };
  }).sort((a, b) => b.bytesPerSecond - a.bytesPerSecond || a.code.localeCompare(b.code));

  // Camera frame: the local network below and the globe above it.
  const focus: Vec3 = [0, globe.center[1] * .56, globe.center[2] * .5];
  const extent = Math.max(Math.hypot(lanRadius, focus[1], focus[2]), length(sub(globe.center, focus)) + globe.radius * 1.35) + 1;
  return { nodes: [...local, ...remoteNodes], links: [...links.values()].sort((a, b) => a.id.localeCompare(b.id)), groups, globe, countries, lanRadius, focus, extent };
}

function layoutGroups(local: TopologyNode[]): TopologyGroup[] {
  const byBlock = new Map<string, { block: ReturnType<typeof addressBlock>; nodes: TopologyNode[] }>();
  for (const node of local) {
    const block = addressBlock(node.ip);
    const entry = byBlock.get(block.id) ?? { block, nodes: [] };
    entry.nodes.push(node); byBlock.set(block.id, entry);
  }
  const entries = [...byBlock.values()].sort((a, b) => a.block.sort.localeCompare(b.block.sort));
  // Inside a plate: infrastructure at the back, clients toward the viewer; inventory before observed-only hosts.
  const plates = entries.map(({ block, nodes }) => {
    nodes.sort((a, b) => roleOrder.indexOf(a.role) - roleOrder.indexOf(b.role) || Number(a.kind === "observed") - Number(b.kind === "observed") || ipSortKey(a.ip).localeCompare(ipSortKey(b.ip)) || a.label.localeCompare(b.label) || a.id.localeCompare(b.id));
    const columns = Math.max(3, Math.ceil(Math.sqrt(nodes.length * 1.8)));
    const bands: TopologyNode[][] = [];
    for (const role of roleOrder) {
      const members = nodes.filter(n => n.role === role);
      for (let i = 0; i < members.length; i += columns) bands.push(members.slice(i, i + columns));
    }
    const width = columns * SPACING + PLATE_PADDING * 2, depth = Math.max(1, bands.length) * SPACING + PLATE_PADDING * 2;
    return { id: block.id, label: block.label, count: nodes.length, bands, columns, width, depth };
  });
  // Wrap plates into rows so large inventories stay compact rather than one endless strip.
  const area = plates.reduce((sum, p) => sum + (p.width + PLATE_GAP) * (p.depth + PLATE_GAP), 0);
  const maxRowWidth = Math.max(40, Math.sqrt(area) * 1.25, ...plates.map(p => p.width));
  const rows: (typeof plates)[] = [];
  let row: typeof plates = [], rowWidth = 0;
  for (const plate of plates) {
    if (row.length && rowWidth + PLATE_GAP + plate.width > maxRowWidth) { rows.push(row); row = []; rowWidth = 0; }
    rowWidth += (row.length ? PLATE_GAP : 0) + plate.width; row.push(plate);
  }
  if (row.length) rows.push(row);
  const totalDepth = rows.reduce((sum, r) => sum + Math.max(...r.map(p => p.depth)), 0) + PLATE_GAP * Math.max(0, rows.length - 1);
  const groups: TopologyGroup[] = [];
  let z = -totalDepth / 2;
  for (const r of rows) {
    const width = r.reduce((sum, p) => sum + p.width, 0) + PLATE_GAP * (r.length - 1), depth = Math.max(...r.map(p => p.depth));
    let x = -width / 2;
    for (const plate of r) {
      const cx = x + plate.width / 2, cz = z + depth / 2;
      plate.bands.forEach((members, bandIndex) => members.forEach((node, column) => {
        const offset = (column - (members.length - 1) / 2) * SPACING;
        node.groupId = plate.id;
        node.position = [cx + offset, 0, cz - plate.depth / 2 + PLATE_PADDING + SPACING * (bandIndex + .5)];
      }));
      groups.push({ id: plate.id, label: plate.label, center: [cx, cz], size: [plate.width, plate.depth], count: plate.count });
      x += plate.width + PLATE_GAP;
    }
    z += depth + PLATE_GAP;
  }
  return groups;
}

// --- Globe geometry. geoDirection matches three.js SphereGeometry UVs, so an equirectangular
// texture, pins and arcs all agree on where a latitude/longitude is.
const DEG = Math.PI / 180, TILT = 20 * DEG, DEFAULT_VIEW: GeoPoint = { lat: 30, lon: 10 };
export const PIN_HEIGHT = 1.06, PIN_STACK = .035, PIN_STACK_MAX = 6, PATH_SAMPLES = 48;
const PIN_SPACING = .55, GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));
const add = (a: Vec3, b: Vec3, scale = 1): Vec3 => [a[0] + b[0] * scale, a[1] + b[1] * scale, a[2] + b[2] * scale];
const sub = (a: Vec3, b: Vec3): Vec3 => [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
const dot = (a: Vec3, b: Vec3) => a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
const cross = (a: Vec3, b: Vec3): Vec3 => [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
const length = (a: Vec3) => Math.hypot(a[0], a[1], a[2]);
const normalize = (a: Vec3): Vec3 => { const l = length(a) || 1; return [a[0] / l, a[1] / l, a[2] / l]; };

export function geoDirection(lat: number, lon: number): Vec3 {
  const phi = (lon + 180) * DEG, theta = (90 - lat) * DEG;
  return [-Math.cos(phi) * Math.sin(theta), Math.cos(theta), Math.sin(phi) * Math.sin(theta)];
}
// Rotation about Y by alpha, then about X by gamma (three.js Euler(gamma, alpha, 0, "XYZ")).
export function rotate([x, y, z]: Vec3, alpha: number, gamma: number): Vec3 {
  const x1 = x * Math.cos(alpha) + z * Math.sin(alpha), z1 = -x * Math.sin(alpha) + z * Math.cos(alpha);
  return [x1, y * Math.cos(gamma) - z1 * Math.sin(gamma), y * Math.sin(gamma) + z1 * Math.cos(gamma)];
}
// Turns the globe so the local site faces the network and default camera, slightly below the equator line of sight.
export function globeOrientation(site: GeoPoint | null) {
  const view = site ?? DEFAULT_VIEW, [x, , z] = geoDirection(0, view.lon);
  return { alpha: -Math.atan2(x, z), gamma: view.lat * DEG + TILT };
}
export function globePoint(globe: Pick<Globe, "center" | "radius" | "alpha" | "gamma">, lat: number, lon: number, scale = 1): Vec3 {
  return add(globe.center, rotate(geoDirection(lat, lon), globe.alpha, globe.gamma), globe.radius * scale);
}
function createGlobe(lanRadius: number, site: GeoPoint | null): Globe {
  const radius = Math.min(20, Math.max(7, lanRadius * .62)), center: Vec3 = [0, radius + 7 + lanRadius * .15, -lanRadius * .35];
  const { alpha, gamma } = globeOrientation(site), facing: Vec3 = [0, -Math.sin(TILT), Math.cos(TILT)];
  const base = { radius, center, alpha, gamma };
  // Without a local site the uplink floats off the surface, so no location is implied.
  const uplink = site ? globePoint(base, site.lat, site.lon) : add(center, facing, radius * 1.3);
  return { ...base, site, uplink, unlocated: add(center, [-1.55, -.3, .25], radius) };
}
// Vogel spiral around a location, spaced by pin size on the globe surface (display aid only).
function spiralOffset(lat: number, lon: number, column: number, radius: number): [number, number] {
  const distance = PIN_SPACING / radius / DEG * Math.sqrt(column), angle = column * GOLDEN_ANGLE;
  const shiftedLat = Math.max(-89, Math.min(89, lat + distance * Math.cos(angle)));
  return [shiftedLat, lon + distance * Math.sin(angle) / Math.max(.2, Math.cos(shiftedLat * DEG))];
}
function unlocatedPoint(ip: string, globe: Globe): Vec3 {
  const angle = hash(ip) * Math.PI * 2, r = 1 + hash(ip + "#") * 1.6;
  return add(globe.unlocated, [Math.cos(angle) * r, (hash(ip + "@") - .5) * 1.2, Math.sin(angle) * r]);
}

// Route of a link: a cable rising from the device into the uplink, then a great-circle arc
// lifted above the globe to the pin. Resampled evenly so pulses move at constant speed.
export function linkPath(globe: Globe, start: Vec3, remote: TopologyNode, direction: TrafficDirection): Vec3[] {
  const u = globe.uplink, points: Vec3[] = [];
  const control: Vec3 = [start[0], start[1] + (u[1] - start[1]) * .62, start[2]];
  for (let i = 0; i <= 24; i++) {
    const t = i / 24, w = 1 - t;
    points.push([0, 1, 2].map(k => w * w * start[k] + 2 * w * t * control[k] + t * t * u[k]) as Vec3);
  }
  const end = remote.position;
  if (remote.located) {
    const from = sub(u, globe.center), to = sub(end, globe.center), a = normalize(from), b = normalize(to);
    const angle = Math.acos(Math.min(1, Math.max(-1, dot(a, b))));
    // Near-antipodal routes have no unique great circle; pass over the globe's top instead.
    const via = angle > Math.PI - .05 ? [a, normalize(cross(a, Math.abs(a[1]) < .9 ? [0, 1, 0] : [1, 0, 0])), b] : [a, b];
    const lift = globe.radius * (.04 + .2 * angle / Math.PI), steps = 64;
    for (let i = 1; i <= steps; i++) {
      const t = i / steps, leg = Math.min(via.length - 2, Math.floor(t * (via.length - 1))), local = t * (via.length - 1) - leg;
      const dir = slerp(via[leg], via[leg + 1], local), r = length(from) + (length(to) - length(from)) * t + lift * Math.sin(Math.PI * t);
      points.push(add(globe.center, dir, r));
    }
  } else {
    const mid: Vec3 = [(u[0] + end[0]) / 2, Math.max(u[1], end[1]) + 2, (u[2] + end[2]) / 2];
    for (let i = 1; i <= 24; i++) {
      const t = i / 24, w = 1 - t;
      points.push([0, 1, 2].map(k => w * w * u[k] + 2 * w * t * mid[k] + t * t * end[k]) as Vec3);
    }
  }
  const path = resample(points, PATH_SAMPLES);
  return direction === "inbound" ? path.reverse() : path;
}
function slerp(a: Vec3, b: Vec3, t: number): Vec3 {
  const angle = Math.acos(Math.min(1, Math.max(-1, dot(a, b))));
  if (angle < 1e-5) return a;
  const s = Math.sin(angle), wa = Math.sin((1 - t) * angle) / s, wb = Math.sin(t * angle) / s;
  return [a[0] * wa + b[0] * wb, a[1] * wa + b[1] * wb, a[2] * wa + b[2] * wb];
}
function resample(points: Vec3[], count: number): Vec3[] {
  const distances = [0];
  for (let i = 1; i < points.length; i++) distances.push(distances[i - 1] + length(sub(points[i], points[i - 1])));
  const total = distances.at(-1)!, result: Vec3[] = [];
  for (let i = 0, j = 1; i < count; i++) {
    const target = total * i / (count - 1);
    while (j < points.length - 1 && distances[j] < target) j++;
    const span = distances[j] - distances[j - 1] || 1, t = Math.min(1, Math.max(0, (target - distances[j - 1]) / span));
    result.push(add(points[j - 1], sub(points[j], points[j - 1]), t));
  }
  return result;
}

// Emphasis for hover/selection: the node, its links and their far ends.
export function neighborhood(topology: Topology, nodeId: string | null, flowId: string | null) {
  const nodes = new Set<string>(), links = new Set<string>();
  for (const link of topology.links) {
    if ((nodeId && (link.source === nodeId || link.target === nodeId)) || (flowId && link.flowIds.includes(flowId))) {
      links.add(link.id); nodes.add(link.source); nodes.add(link.target);
    }
  }
  if (nodeId) nodes.add(nodeId);
  return { nodes, links, active: !!nodeId || !!flowId };
}
