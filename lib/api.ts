import type { AgentEnrollment, DeviceDetail, DeviceFacets, DeviceList, DiscoveryScan, DiscoveryState, DiscoveryTarget, EventList, ManagementAgent, RemoteCommand, RemoteEndpoint, ServiceStatus, Session, TrafficSnapshot } from "./types";

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

export const api = {
  status: () => request<ServiceStatus>("/api/v1/status"),
  liveTraffic: () => request<TrafficSnapshot>("/api/v1/traffic/live"),
  events: (limit = 8, entityId?: string) => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (entityId) params.set("entityId", entityId);
    return request<EventList>(`/api/v1/events?${params}`);
  },
  devices: (query: { search?: string; status?: string; classification?: string; sort?: string; direction?: "asc" | "desc"; limit?: number; offset?: number }) => {
    const params = new URLSearchParams();
    Object.entries(query).forEach(([key, value]) => {
      if (value !== undefined && value !== "") params.set(key, String(value));
    });
    return request<DeviceList>(`/api/v1/devices?${params}`);
  },
  device: (id: string) => request<DeviceDetail>(`/api/v1/devices/${encodeURIComponent(id)}`),
  deviceFacets: () => request<DeviceFacets>("/api/v1/devices/facets"),
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
  deviceRemote: (deviceId: string) => request<{ endpoints: RemoteEndpoint[] }>(`/api/v1/devices/${encodeURIComponent(deviceId)}/remote`),
  deviceCommands: (deviceId: string) => request<{ items: RemoteCommand[] }>(`/api/v1/devices/${encodeURIComponent(deviceId)}/commands`),
  createCommand: (agentId: string, type: string, parameters: Record<string, unknown> = {}) =>
    request<RemoteCommand>(`/api/v1/agents/${encodeURIComponent(agentId)}/commands`, { method: "POST", body: JSON.stringify({ type, parameters }) }),
  command: (id: string) => request<RemoteCommand>(`/api/v1/remote/commands/${encodeURIComponent(id)}`),
  cancelCommand: (id: string) => request<RemoteCommand>(`/api/v1/remote/commands/${encodeURIComponent(id)}/cancel`, { method: "POST", body: "{}" }),
};
