import type { DeviceStatus, DeviceSummary, TrafficDirection, TrafficFlow } from "@/lib/types";

// Deterministic 3D topology derived only from observed NERVE data. Plates group devices by
// address block (derived, not a verified segment); the horizon ring places remote endpoints by
// approximate longitude/latitude. Nothing here invents links: every link is a measured flow.

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
  countryCode?: string;
  countryName?: string;
  city?: string;
  position: Vec3;
  bytesPerSecond: number;
  flowIds: string[];
};
export type TopologyLink = { id: string; source: string; target: string; direction: TrafficDirection; bytesPerSecond: number; flowIds: string[]; applications: string[] };
export type TopologyGroup = { id: string; label: string; center: [number, number]; size: [number, number]; count: number };
export type HorizonLabel = { code: string; name: string; position: Vec3; bytesPerSecond: number };
export type Topology = {
  nodes: TopologyNode[];
  links: TopologyLink[];
  groups: TopologyGroup[];
  horizon: { radius: number; height: number; band: number; referenceLongitude: number; unlocated: Vec3 };
  countries: HorizonLabel[];
  lanRadius: number;
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

type Options = { devices: DeviceSummary[]; flows: TrafficFlow[]; deviceFilter?: "all" | "traffic" | "online"; referenceLongitude?: number };

export function buildTopology({ devices, flows, deviceFilter = "all", referenceLongitude = 0 }: Options): Topology {
  const localByIP = new Map<string, TopologyNode>();
  const local: TopologyNode[] = [];
  const trafficIPs = new Set(flows.map(f => f.localIp));
  for (const device of devices) {
    if (deviceFilter === "online" && device.status !== "online") continue;
    if (deviceFilter === "traffic" && !(device.primaryIpAddress && trafficIPs.has(device.primaryIpAddress))) continue;
    const node: TopologyNode = { id: `device:${device.id}`, kind: "device", role: roleFor(device.classification), label: device.name, ip: device.primaryIpAddress, status: device.status, device, position: [0, 0, 0], bytesPerSecond: 0, flowIds: [] };
    local.push(node);
    if (device.primaryIpAddress && !localByIP.has(device.primaryIpAddress)) localByIP.set(device.primaryIpAddress, node);
  }
  // Hosts seen in traffic but absent from inventory stay visible, marked as observed-only.
  for (const ip of [...trafficIPs].sort()) {
    if (localByIP.has(ip)) continue;
    const node: TopologyNode = { id: `host:${ip}`, kind: "observed", role: "unknown", label: ip, ip, status: "unknown", position: [0, 0, 0], bytesPerSecond: 0, flowIds: [] };
    local.push(node); localByIP.set(ip, node);
  }

  const groups = layoutGroups(local);
  const lanRadius = Math.max(8, ...groups.map(g => Math.hypot(Math.abs(g.center[0]) + g.size[0] / 2, Math.abs(g.center[1]) + g.size[1] / 2)));
  const radius = lanRadius * 1.3 + 8, height = Math.max(9, radius * .4), band = height * .32;
  const horizon = { radius, height, band, referenceLongitude, unlocated: [0, height + band + Math.max(4, height * .35), 0] as Vec3 };

  const remotes = new Map<string, TopologyNode>();
  const links = new Map<string, TopologyLink>();
  const countryTotals = new Map<string, { name: string; bytes: number; x: number; y: number; z: number; n: number }>();
  for (const flow of flows) {
    const source = localByIP.get(flow.localIp);
    if (!source) continue;
    let remote = remotes.get(flow.remoteIp);
    if (!remote) {
      const located = flow.remoteLatitude !== undefined && flow.remoteLongitude !== undefined && Number.isFinite(flow.remoteLatitude) && Number.isFinite(flow.remoteLongitude);
      remote = { id: `remote:${flow.remoteIp}`, kind: "remote", role: "unknown", label: flow.remoteIp, ip: flow.remoteIp, status: "online", located, countryCode: flow.remoteCountryCode, countryName: flow.remoteCountryName, city: flow.remoteCity, position: located ? horizonPoint(flow.remoteLatitude!, flow.remoteLongitude!, flow.remoteIp, horizon) : unlocatedPoint(flow.remoteIp, horizon), bytesPerSecond: 0, flowIds: [] };
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
  for (const remote of remotes.values()) {
    if (!remote.located || !remote.countryCode) continue;
    const total = countryTotals.get(remote.countryCode) ?? { name: remote.countryName ?? remote.countryCode, bytes: 0, x: 0, y: 0, z: 0, n: 0 };
    total.bytes += remote.bytesPerSecond; total.x += remote.position[0]; total.y += remote.position[1]; total.z += remote.position[2]; total.n++;
    countryTotals.set(remote.countryCode, total);
  }
  const countries = [...countryTotals].map(([code, t]) => ({ code, name: t.name, bytesPerSecond: t.bytes, position: [t.x / t.n, t.y / t.n + band * .55, t.z / t.n] as Vec3 }))
    .sort((a, b) => b.bytesPerSecond - a.bytesPerSecond || a.code.localeCompare(b.code));

  const remoteNodes = [...remotes.values()].sort((a, b) => a.id.localeCompare(b.id));
  return { nodes: [...local, ...remoteNodes], links: [...links.values()].sort((a, b) => a.id.localeCompare(b.id)), groups, horizon, countries, lanRadius, extent: radius + 2 };
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

// The horizon is an equirectangular world wrapped on a cylinder: angle follows longitude
// (the local site's longitude faces the default camera), height follows latitude.
export function cylinderPoint(lat: number, lon: number, horizon: Pick<Topology["horizon"], "radius" | "height" | "band" | "referenceLongitude">, radius = horizon.radius): Vec3 {
  const angle = (lon - horizon.referenceLongitude) * Math.PI / 180;
  return [Math.sin(angle) * radius, horizon.height + lat / 90 * horizon.band, Math.cos(angle) * radius];
}
function horizonPoint(lat: number, lon: number, ip: string, horizon: Topology["horizon"]) {
  // Endpoints in the same city would overlap exactly; a tiny stable offset keeps them pickable.
  const jitter = hash(ip);
  return cylinderPoint(lat + (jitter - .5) * 3, lon + (hash(ip + "#") - .5) * 3, horizon, horizon.radius + (jitter - .5) * .8);
}
function unlocatedPoint(ip: string, horizon: Topology["horizon"]): Vec3 {
  const angle = hash(ip) * Math.PI * 2, r = 1.2 + hash(ip + "#") * 2.4;
  return [Math.cos(angle) * r, horizon.unlocated[1] + (hash(ip + "@") - .5) * 1.2, Math.sin(angle) * r];
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
