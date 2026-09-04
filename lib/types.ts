export type ServiceStatus = {
  status: "ok" | "degraded";
  version: string;
  uptimeSeconds: number;
  database: { status: "ok" | "unavailable"; latencyMs?: number };
  time: string;
};

export type NerveEvent = {
  id: string;
  entityId?: string;
  type: string;
  severity: "info" | "warning" | "critical";
  category: string;
  message: string;
  occurredAt: string;
};

export type EventList = { items: NerveEvent[] };
export type Session = { authenticated: boolean; user?: { id: string; email: string; role: string } };

export type DeviceStatus = "online" | "offline" | "warning" | "unknown" | "stopped" | "starting";

export type DeviceSummary = {
  id: string;
  name: string;
  status: DeviceStatus;
  classification: string;
  hostname?: string;
  primaryIpAddress?: string;
  primaryMac?: string;
  vendor?: string;
  operatingSystem?: string;
  firstSeenAt: string;
  lastSeenAt: string;
};

export type Device = DeviceSummary & {
  model?: string;
  serialNumber?: string;
  owner?: string;
  location?: string;
  notes?: string;
  approvedAt?: string;
};

export type IPAddress = {
  id: string;
  address: string;
  version: number;
  isPrimary: boolean;
  interfaceId: string;
  interfaceName: string;
  validFrom: string;
  validUntil?: string;
};

export type NetworkInterface = {
  id: string;
  name: string;
  macAddress?: string;
  mediaType?: string;
  mtu?: number;
  speedBps?: number;
  isUp?: boolean;
  addresses: IPAddress[];
};

export type ServiceEndpoint = {
  id: string;
  serviceId?: string;
  name: string;
  serviceType?: string;
  protocol: "tcp" | "udp";
  port: number;
  exposure: "local" | "lan" | "internet" | "unknown";
  state: "open" | "closed" | "filtered" | "unknown";
  firstSeenAt: string;
  lastSeenAt: string;
};

export type Relationship = {
  id: string;
  type: string;
  direction: "inbound" | "outbound";
  relatedEntity: { id: string; name: string; kind: string; status: DeviceStatus };
  source: string;
  confidence: number;
  firstSeenAt: string;
  lastSeenAt: string;
  manuallyConfirmed: boolean;
};

export type DeviceList = { items: DeviceSummary[]; total: number; limit: number; offset: number };
export type DeviceFacets = { statuses: string[]; classifications: string[] };
export type DeviceDetail = { device: Device; interfaces: NetworkInterface[]; ipAddresses: IPAddress[]; services: ServiceEndpoint[]; relationships: Relationship[] };
