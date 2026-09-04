import type { EventList, ServiceStatus, Session } from "./types";

export class APIError extends Error {
  constructor(public status: number, message: string) {
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
    const body = (await response.json().catch(() => null)) as { error?: { message?: string } } | null;
    throw new APIError(response.status, body?.error?.message ?? `Request failed with status ${response.status}.`);
  }
  return response.json() as Promise<T>;
}

export const api = {
  status: () => request<ServiceStatus>("/api/v1/status"),
  events: () => request<EventList>("/api/v1/events?limit=8"),
  session: () => request<Session>("/api/v1/auth/session"),
  login: (email: string, password: string) => request<Session>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ email, password }) }),
  setup: (email: string, password: string) => request<Session>("/api/v1/auth/bootstrap", { method: "POST", body: JSON.stringify({ email, password }) }),
};

