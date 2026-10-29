import type { AgentDeploymentKey, AgentEnrollment,Device, DeviceDetail, DeviceFacets, DeviceList, DeviceMetadataUpdate, DeviceVocabulary, DuplicateCandidate, IdentityReport, TrustState, DiscoveryScan, DiscoveryState, DiscoveryTarget, EventList, ManagementAgent, MeshCentralStatus, RemoteCommand, RemoteEndpoint, ServiceStatus, Session, TrafficSnapshot } from "./types";

import type { TrafficCapture } from "./types";

export class APIError extends Error {
  constructor(public status: number, message: string, public code?: string, public capture?: TrafficCapture) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "include",
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: { message?: string; code?: string }; capture?: TrafficCapture } | null;
    throw new APIError(response.status, body?.error?.message ?? `Request failed with status ${response.status}.`, body?.error?.code, body?.capture);
  }
  return response.json() as Promise<T>;
}

// requestNoContent is for endpoints that answer 204 without a body.
async function requestNoContent(path: string, init?: RequestInit): Promise<void> {
  const response = await fetch(path, { ...init, credentials: "include", headers: { "Content-Type": "application/json", ...init?.headers } });
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: { message?: string; code?: string } } | null;
    throw new APIError(response.status, body?.error?.message ?? `Request failed with status ${response.status}.`, body?.error?.code);
  }
}

export const api = {
  status: () => request<ServiceStatus>("/api/v1/status"),
  liveTraffic: () => request<TrafficSnapshot>("/api/v1/traffic/live"),
  events: (limit = 8, entityId?: string) => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (entityId) params.set("entityId", entityId);
    return request<EventList>(`/api/v1/events?${params}`);
  },
  devices: (query: { search?: string; status?: string; classification?: string; trust?: string; tag?: string; sort?: string; direction?: "asc" | "desc"; limit?: number; offset?: number }) => {
    const params = new URLSearchParams();
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== "") params.set(key, String(value));
    });
    return request<DeviceList>(`/api/v1/devices?${params}`);
  },
  device: (id: string) => request<DeviceDetail>(`/api/v1/devices/${encodeURIComponent(id)}`),
  deviceFacets: () => request<DeviceFacets>("/api/v1/devices/facets"),
  deviceVocabulary: () => request<DeviceVocabulary>("/api/v1/devices/vocabulary"),
  deviceIdentity: (id: string) => request<IdentityReport>(`/api/v1/devices/${encodeURIComponent(id)}/identity`),
  updateDeviceMetadata: (id: string, update: DeviceMetadataUpdate) => request<Device>(`/api/v1/devices/${encodeURIComponent(id)}/metadata`, { method: "POST", body: JSON.stringify(update) }),
  setDeviceTrust: (id: string, state: TrustState, reason = "") => request<Device>(`/api/v1/devices/${encodeURIComponent(id)}/trust`, { method: "POST", body: JSON.stringify({ state, reason }) }),
  deviceDuplicates: (deviceId?: string) => request<{ items: DuplicateCandidate[] }>(`/api/v1/devices/duplicates${deviceId ? `?deviceId=${encodeURIComponent(deviceId)}` : ""}`),
  dismissDuplicate: (deviceA: string, deviceB: string, reason = "") => requestNoContent("/api/v1/devices/duplicates/dismiss", { method: "POST", body: JSON.stringify({ deviceA, deviceB, reason }) }),
  mergeDevice: (targetId: string, sourceDeviceId: string, reason = "") => request<{ mergeId: string; targetDeviceId: string }>(`/api/v1/devices/${encodeURIComponent(targetId)}/merge`, { method: "POST", body: JSON.stringify({ sourceDeviceId, reason }) }),
  splitDevice: (id: string, input: { displayName?: string; interfaceIds: string[]; identityIds?: string[]; agent?: string; reason?: string }) => request<{ splitId: string; newDeviceId: string }>(`/api/v1/devices/${encodeURIComponent(id)}/split`, { method: "POST", body: JSON.stringify(input) }),
  discovery: () => request<DiscoveryState>("/api/v1/discovery"),
  saveDiscoveryTarget: (cidr: string, name = "Local network") => request<DiscoveryTarget>("/api/v1/discovery/target", { method: "POST", body: JSON.stringify({ cidr, name }) }),
  startDiscoveryScan: (targetId: string) => request<DiscoveryScan>("/api/v1/discovery/scans", { method: "POST", body: JSON.stringify({ targetId }) }),
  stopDiscoveryScan: (targetId: string) => request<DiscoveryScan>("/api/v1/discovery/scans", { method: "POST", body: JSON.stringify({ targetId, cancelActive: true }) }),
  session: () => request<Session>("/api/v1/auth/session"),
  login: (email: string, password: string) => request<Session>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ email, password }) }),
  setup: (email: string, password: string) => request<Session>("/api/v1/auth/bootstrap", { method: "POST", body: JSON.stringify({ email, password }) }),
  agents: () => request<{ items: ManagementAgent[] }>("/api/v1/agents"),
  revokeAgent: (id: string) => request<ManagementAgent>(`/api/v1/agents/${encodeURIComponent(id)}/revoke`, { method: "POST", body: "{}" }),
  removeAgent: (id: string) => request<ManagementAgent>(`/api/v1/agents/${encodeURIComponent(id)}/remove`, { method: "POST", body: "{}" }),
  updateAssociation: (id: string, action: "confirm" | "dismiss" | "reevaluate", deviceId?: string) =>
    request<ManagementAgent>(`/api/v1/agents/${encodeURIComponent(id)}/association`, { method: "POST", body: JSON.stringify({ action, deviceId: deviceId ?? "" }) }),
  enrollments: () => request<{ items: AgentEnrollment[] }>("/api/v1/agent-enrollments"),
  createEnrollment: (input: { displayName?: string; deviceId?: string; agentId?: string; expiresInMinutes?: number }) =>
    request<{ enrollment: AgentEnrollment; token: string }>("/api/v1/agent-enrollments", { method: "POST", body: JSON.stringify(input) }),
  revokeEnrollment: (id: string) => request<AgentEnrollment>(`/api/v1/agent-enrollments/${encodeURIComponent(id)}/revoke`, { method: "POST", body: "{}" }),
  approveAgent: (id: string) => request<ManagementAgent>(`/api/v1/agents/${encodeURIComponent(id)}/approve`, { method: "POST", body: "{}" }),
  deploymentKeys: () => request<{ items: AgentDeploymentKey[] }>("/api/v1/agent-deployment-keys"),
  createDeploymentKey: (input: { name: string; approval: "manual" | "automatic"; maxUses: number; expiresInDays: number }) =>
    request<{ deploymentKey: AgentDeploymentKey; key: string }>("/api/v1/agent-deployment-keys", { method: "POST", body: JSON.stringify(input) }),
  revokeDeploymentKey: (id: string) => request<AgentDeploymentKey>(`/api/v1/agent-deployment-keys/${encodeURIComponent(id)}/revoke`, { method: "POST", body: "{}" }),
  deviceRemote: (deviceId: string) => request<{ endpoints: RemoteEndpoint[] }>(`/api/v1/devices/${encodeURIComponent(deviceId)}/remote`),
  deviceCommands: (deviceId: string) => request<{ items: RemoteCommand[] }>(`/api/v1/devices/${encodeURIComponent(deviceId)}/commands`),
  createCommand: (agentId: string, type: string, parameters: Record<string, unknown> = {}) =>
    request<RemoteCommand>(`/api/v1/agents/${encodeURIComponent(agentId)}/commands`, { method: "POST", body: JSON.stringify({ type, parameters }) }),
  command: (id: string) => request<RemoteCommand>(`/api/v1/remote/commands/${encodeURIComponent(id)}`),
  cancelCommand: (id: string) => request<RemoteCommand>(`/api/v1/remote/commands/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" }),
  meshCentralStatus: () => request<MeshCentralStatus>("/api/v1/integrations/meshcentral"),
  syncMeshCentral: () => request<MeshCentralStatus>("/api/v1/integrations/meshcentral/sync", { method: "POST", body: "{}" }),
  updateMeshCentralBinding: (deviceId: string, action: "confirm" | "unbind", nodeId: string) =>
    requestNoContent(`/api/v1/devices/${encodeURIComponent(deviceId)}/meshcentral/binding`, { method: "POST", body: JSON.stringify({ action, nodeId }) }),
};
