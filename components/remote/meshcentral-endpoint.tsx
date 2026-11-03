"use client";

import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { APIError, api } from "@/lib/api";
import type { RemoteEndpoint } from "@/lib/types";

const bindingLabels: Record<string, string> = {
  confirmed: "Confirmed by an operator",
  candidate: "Candidate · matching MAC address, needs confirmation",
  weak: "Hostname match only · not enough to bind",
};

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(new Date(value));
}

function presence(endpoint: RemoteEndpoint) {
  if (endpoint.status === "absent") return { label: "Not in the latest MeshCentral listing", dot: "warning" };
  return endpoint.online ? { label: "MeshAgent connected", dot: "online" } : { label: "MeshAgent not connected", dot: "offline" };
}

// MeshCentralEndpoint shows one MeshCentral node next to the native agent.
// Bindings are never automatic: an operator confirms MAC evidence here.
export function MeshCentralEndpoint({ deviceId, endpoint, canManage, onChanged }: {
  deviceId: string; endpoint: RemoteEndpoint; canManage: boolean; onChanged: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const state = presence(endpoint);

  async function act(action: () => Promise<unknown>, failure: string) {
    setBusy(true); setError(null);
    try { await action(); await onChanged(); } catch (requestError) {
      setError(requestError instanceof APIError ? requestError.message : failure);
    } finally { setBusy(false); }
  }

  return <section className="remote-endpoint">
    <div className="detail-section-heading remote-heading"><h2>MeshCentral · {endpoint.displayName}</h2>
      {canManage && <button className="button" type="button" disabled={busy} aria-busy={busy}
        onClick={() => void act(() => api.syncMeshCentral(), "MeshCentral could not be synced.")}><RefreshCw size={14} aria-hidden="true" />Sync</button>}</div>
    {error && <div className="form-error" role="alert">{error}</div>}
    <dl className="property-list">
      <div><dt>Node</dt><dd className="mono">{endpoint.endpointId}</dd></div>
      <div><dt>Presence</dt><dd><span className="device-status"><span className="status-dot" data-status={state.dot} aria-hidden="true" />{state.label}</span></dd></div>
      <div><dt>Operating system</dt><dd>{endpoint.version || "—"}</dd></div>
      <div><dt>Last agent connection</dt><dd>{formatDate(endpoint.lastSeenAt)}</dd></div>
      <div><dt>Binding</dt><dd>{bindingLabels[endpoint.association] ?? endpoint.association}</dd></div>
      <div><dt>Evidence</dt><dd className="mono">{endpoint.evidence?.length ? endpoint.evidence.map((signal) => `${signal.type} ${signal.value}`).join(", ") : "—"}</dd></div>
      <div><dt>Capabilities</dt><dd>{endpoint.capabilities.map((capability) => capability.label).join(", ")} · not usable from NERVE yet</dd></div>
    </dl>
    {endpoint.status === "absent" && endpoint.association === "confirmed" && <p className="remote-note">
      A reinstalled MeshAgent gets a new node ID and does not inherit this binding. Remove it and confirm the new node if it appears.</p>}
    {canManage && (endpoint.association === "candidate" || endpoint.association === "confirmed") && <div className="remote-actions">
      {endpoint.association === "candidate" && <button className="button button-primary" type="button" disabled={busy}
        onClick={() => void act(() => api.updateMeshCentralBinding(deviceId, "confirm", endpoint.endpointId), "The binding could not be confirmed.")}>Confirm binding</button>}
      {endpoint.association === "confirmed" && <button className="button" type="button" disabled={busy}
        onClick={() => void act(() => api.updateMeshCentralBinding(deviceId, "unbind", endpoint.endpointId), "The binding could not be removed.")}>Remove binding</button>}
    </div>}
  </section>;
}
