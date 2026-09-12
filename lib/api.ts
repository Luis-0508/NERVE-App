import type { DeviceDetail, DeviceFacets, DeviceList, DiscoveryScan, DiscoveryState, DiscoveryTarget, EventList, ServiceStatus, Session, TrafficSnapshot } from "./types";

export class APIError extends Error {
  constructor(public status: number, message: string, public code?: string) {
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
    const body = (await response.json().catch(() => null)) as { error?: { message?: string; code?: string } } | null;
    throw new APIError(response.status, body?.error?.message ?? `Request failed with status ${response.status}.`, body?.error?.code);
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
};
