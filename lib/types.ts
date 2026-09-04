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

