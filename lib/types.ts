import type { AgentConnection } from "@/lib/agent-status";

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
export type Session = { authenticated: boolean; user?: { id: string; email: string; role: string; permissions: string[] } };

export type DeviceStatus = "online" | "offline" | "warning" | "unknown" | "stopped" | "starting";

export type TrustState = "unknown" | "reviewed" | "trusted" | "blocked";

// The effective device: name and classification are resolved by Core
// (operator override first, then discovered values).
export type DeviceSummary = {
  id: string;
  name: string;
  displayName?: string;
  discoveredName?: string;
  status: DeviceStatus;
  classification: string;
  discoveredClassification: string;
  classificationOverridden: boolean;
  trustState: TrustState;
  tags: string[];
  hostname?: string;
  primaryIpAddress?: string;
  /** Current addresses, primary first; bounded by the API. */
  ipAddresses: string[];
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
  metadataUpdatedAt?: string;
  metadataUpdatedBy?: string;
  trustChangedAt?: string;
  trustChangedBy?: string;
  mergedInto?: string;
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
export type DeviceFacets = { statuses: string[]; classifications: string[]; trustStates?: string[]; tags?: string[] };
export type DeviceFact = { attribute: string; source: string; value: string; confidence: number; firstSeenAt: string; lastSeenAt: string; effective: boolean };
export type DeviceDetail = { device: Device; interfaces: NetworkInterface[]; ipAddresses: IPAddress[]; services: ServiceEndpoint[]; relationships: Relationship[]; facts: DeviceFact[] };
export type DeviceRef = { id: string; name: string };
export type EvidenceStrength = "strong" | "moderate" | "weak" | "none";
export type IdentityEvidence = { type: string; value: string; source: string; confidence: number; strength: EvidenceStrength; note?: string; firstSeenAt: string; lastSeenAt: string; manuallyConfirmed: boolean; sharedWith: DeviceRef[] };
export type IdentityReport = {
  deviceId: string;
  mergedInto?: string;
  strength: EvidenceStrength;
  summary: string;
  evidence: IdentityEvidence[];
  agents: Array<{ id: string; displayName: string; status: string; online: boolean; association: string; lastSeenAt?: string }>;
  facts: DeviceFact[];
  discoveryMatches: Array<{ reason: string; count: number; last: string }>;
  classification?: ClassificationSuggestion;
};
export type ClassificationSuggestion = { classification: string; source: string; confidence: number; reason: string };
export type DuplicateCandidate = { deviceA: DeviceRef; deviceB: DeviceRef; confidence: "high" | "medium"; reasons: string[]; signals: Array<{ type: string; value: string; fromAgent: boolean }> };
export type DeviceVocabulary = { trustStates: TrustState[]; classifications: string[] };
export type DeviceMetadataUpdate = { displayName?: string | null; notes?: string | null; owner?: string | null; location?: string | null; classification?: string | null; tags?: string[] };

export type DiscoveryTarget = { id: string; name: string; cidr: string; enabled: boolean; createdAt: string; updatedAt: string };
export type DiscoveryAgent = { id: string; agentId?: string; legacyToken: boolean; hostname: string; platform: string; version: string; capabilities: string[]; lastHeartbeatAt: string; lastError?: string; online: boolean };
export type DiscoveryScanError = { address?: string; stage: string; message: string; occurredAt: string };
export type DiscoveryScan = {
  id: string;
  targetId: string;
  targetCidr: string;
  agentId?: string;
  status: "queued" | "running" | "succeeded" | "partial" | "failed";
  requestedAt: string;
  startedAt?: string;
  completedAt?: string;
  addressesTotal: number;
  addressesReachable: number;
  errorCount: number;
  errorMessage?: string;
  errors: DiscoveryScanError[];
};
export type DiscoveryState = { target?: DiscoveryTarget; agent?: DiscoveryAgent; scan?: DiscoveryScan };
export type TrafficDirection = "inbound" | "outbound";
export type TrafficFlow = { id: string; localIp: string; remoteIp: string; localPort: number; remotePort: number; localPorts: number[]; remotePorts: number[]; flowCount: number; direction: TrafficDirection; protocol: string; application: string; bytes: number; bytesPerSecond: number; firstSeenAt: string; lastSeenAt: string; remoteCountryCode?: string; remoteCountryName?: string; remoteLatitude?: number; remoteLongitude?: number; remoteCity?: string; remoteRegion?: string; remoteLocationSource?: "dbip-city-lite" | "country-representative" };
export type TrafficCapture = { mode: string; interfaceId: string; interfaceName?: string; visibility: string; eventsLost?: number; backendStage?: string; backendError?: number };
export type TrafficSnapshot = { capture?: TrafficCapture; generatedAt: string; source: string; flows: TrafficFlow[]; truncated: boolean; sampledFlows: number; sourceTotalFlows: number; summary: { activeFlows: number; aggregatedFlows: number; unlocatedFlows: number; bytesPerSecond: number; inboundBytesPerSecond: number; outboundBytesPerSecond: number } };

export type AssociationStatus = "unassociated" | "candidate" | "automatic" | "confirmed" | "dismissed";
export type AssociationEvidence = { deviceId: string; deviceName?: string; strength: "strong" | "weak" | "operator"; note?: string; signals: Array<{ type: string; value: string }> };

export type ManagementAgent = {
  id: string;
  displayName: string;
  status: "pending" | "active" | "revoked" | "removed";
  online: boolean;
  connection?: AgentConnection;
  runMode?: string;
  processStartedAt?: string;
  hostBootAt?: string;
  lastShutdownAt?: string;
  hostname?: string;
  os?: string;
  osVersion?: string;
  architecture?: string;
  agentVersion?: string;
  capabilities: string[];
  macAddresses: string[];
  ipAddresses: string[];
  lastIp?: string;
  lastError?: string;
  firstEnrolledAt: string;
  enrolledAt: string;
  lastHeartbeatAt?: string;
  lastSeenAt?: string;
  device?: { id: string; name: string };
  association: { status: AssociationStatus; evidence: AssociationEvidence[]; associatedAt?: string };
  credential?: { id: string; fingerprint: string; createdAt: string; rotations: number; lastRotatedAt?: string };
  deploymentKeyId?: string;
};

export type AgentDeploymentKey = {
  id: string;
  name: string;
  approval: "manual" | "automatic";
  maxUses: number;
  usedCount: number;
  createdByEmail?: string;
  createdAt: string;
  expiresAt: string;
  lastUsedAt?: string;
  revokedAt?: string;
  state: "active" | "exhausted" | "expired" | "revoked";
};

export type AgentEnrollment = {
  id: string;
  displayName?: string;
  deviceId?: string;
  targetAgentId?: string;
  createdByEmail?: string;
  createdAt: string;
  expiresAt: string;
  state: "pending" | "used" | "expired" | "revoked";
  usedAt?: string;
  enrolledAgentId?: string;
};

export type RemoteCapability = { name: string; label: string; risk: "read_only" | "privileged" | "interactive"; description: string; executable: boolean };

export type RemoteEndpoint = {
  provider: string;
  endpointId: string;
  displayName: string;
  status: string;
  online: boolean;
  connection?: AgentConnection;
  version?: string;
  lastSeenAt?: string;
  association: AssociationStatus;
  capabilities: RemoteCapability[];
};

export type RemoteCommandStatus = "queued" | "claimed" | "running" | "succeeded" | "failed" | "timed_out" | "cancelled" | "expired";

export type RemoteCommand = {
  id: string;
  provider: string;
  agentId: string;
  agentName: string;
  deviceId?: string;
  type: string;
  parameters: Record<string, unknown>;
  requestedByEmail?: string;
  requestedAt: string;
  expiresAt: string;
  timeoutSeconds: number;
  status: RemoteCommandStatus;
  completedAt?: string;
  failureCode?: string;
  failureReason?: string;
  resultPruned?: boolean;
  result?: { output?: unknown; outputTruncated: boolean; outputBytes: number; submittedAt: string; redactions?: Record<string, number>; outputWithheld?: boolean };
};
