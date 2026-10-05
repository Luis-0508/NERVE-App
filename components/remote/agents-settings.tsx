"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { AgentEnrollment, ManagementAgent } from "@/lib/types";

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function agentState(agent: ManagementAgent) {
  if (agent.status === "removed") return { label: "Removed", dot: "stopped" };
  if (agent.status === "revoked") return { label: "Revoked", dot: "warning" };
  return agent.online ? { label: "Online", dot: "online" } : { label: "Offline", dot: "offline" };
}

export function AgentsSettings({ permissions }: { permissions: string[] }) {
  const canManage = permissions.includes("agents.manage");
  const [agents, setAgents] = useState<ManagementAgent[] | null>(null);
  const [enrollments, setEnrollments] = useState<AgentEnrollment[]>([]);
  const [token, setToken] = useState<{ value: string; expiresAt: string; reenroll: boolean } | null>(null);
  const [name, setName] = useState("");
  const [minutes, setMinutes] = useState(60);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [agentList, enrollmentList] = await Promise.all([api.agents(), canManage ? api.enrollments() : Promise.resolve({ items: [] })]);
      setAgents(agentList.items); setEnrollments(enrollmentList.items); setError(null);
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "Agents could not be loaded.");
    }
  }, [canManage]);
  useEffect(() => { void load(); }, [load]);

  async function act(action: () => Promise<unknown>) {
    setBusy(true); setError(null);
    try { await action(); await load(); } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The action failed.");
    } finally { setBusy(false); }
  }

  async function createEnrollment(agentId?: string) {
    await act(async () => {
      const result = await api.createEnrollment({ displayName: agentId ? undefined : name.trim() || undefined, agentId, expiresInMinutes: minutes });
      setToken({ value: result.token, expiresAt: result.enrollment.expiresAt, reenroll: Boolean(agentId) });
      setName("");
    });
  }

  return <section className="settings-panel agents-settings">
    <div className="settings-row"><div><h2>Management agents</h2><p>Enrolled NERVE agents. Each has its own Ed25519 key; revoking one never affects another.</p></div>
      <div>{error && <div className="form-error" role="alert">{error}</div>}
        {agents === null ? <p className="muted">{error ? "" : "Loading agents…"}</p> : agents.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>Agent</th><th>Status</th><th>Host</th><th>Device</th><th>Version</th><th>Last seen</th><th>Key</th>{canManage && <th />}</tr></thead><tbody>
          {agents.map((agent) => {
            const state = agentState(agent);
            const candidate = agent.association.status === "candidate" && agent.association.evidence.length === 1 ? agent.association.evidence[0] : null;
            return <tr key={agent.id}>
              <td>{agent.displayName}</td>
              <td><span className="device-status"><span className="status-dot" data-status={state.dot} aria-hidden="true" />{state.label}</span></td>
              <td>{agent.hostname ?? "—"}<span className="muted"> · {agent.os ?? "?"}/{agent.architecture ?? "?"}</span></td>
              <td>{agent.device ? <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(agent.device.id)}`}>{agent.device.name}</Link> : <span className="muted">{agent.association.status}</span>}
                {agent.device && <span className="muted"> · {agent.association.status}</span>}
                {candidate && <span className="muted" title={candidate.signals.map((signal) => `${signal.type}: ${signal.value}`).join(", ")}> · candidate {candidate.deviceName}</span>}</td>
              <td className="mono">{agent.agentVersion ?? "—"}</td>
              <td>{formatDate(agent.lastSeenAt)}</td>
              <td className="mono" title={agent.credential?.fingerprint}>{agent.credential ? agent.credential.fingerprint.slice(0, 12) : "—"}</td>
              {canManage && <td className="agent-actions">
                {candidate && <button className="button" type="button" disabled={busy} onClick={() => void act(() => api.updateAssociation(agent.id, "confirm", candidate.deviceId))}>Link</button>}
                {agent.association.status !== "dismissed" && agent.association.status !== "confirmed" && agent.status !== "removed" &&
                  <button className="button" type="button" disabled={busy} onClick={() => void act(() => api.updateAssociation(agent.id, "dismiss"))}>No device</button>}
                {agent.status !== "removed" && <button className="button" type="button" disabled={busy} onClick={() => void createEnrollment(agent.id)}>Re-enroll</button>}
                {agent.status === "active" && <button className="button" type="button" disabled={busy}
                  onClick={() => { if (window.confirm(`Revoke the credential of ${agent.displayName}? Pending commands are cancelled.`)) void act(() => api.revokeAgent(agent.id)); }}>Revoke</button>}
                {agent.status !== "removed" && <button className="button" type="button" disabled={busy}
                  onClick={() => { if (window.confirm(`Remove ${agent.displayName} permanently? Its history is kept.`)) void act(() => api.removeAgent(agent.id)); }}>Remove</button>}
              </td>}
            </tr>;
          })}
        </tbody></table></div> : <p className="muted">No agents are enrolled.</p>}
      </div>
    </div>

    {canManage && <div className="settings-row"><div><h2>Enroll an agent</h2><p>Creates a single-use token. Only its digest is stored; it is shown once.</p></div>
      <div>
        <div className="enroll-form">
          <label className="field"><span>Display name (optional)</span><input className="input" value={name} maxLength={200} onChange={(event) => setName(event.target.value)} /></label>
          <label className="field"><span>Valid for (minutes)</span><input className="input" type="number" min={5} max={1440} value={minutes} onChange={(event) => setMinutes(Number(event.target.value))} /></label>
          <button className="button button-primary" type="button" disabled={busy} onClick={() => void createEnrollment()}>Create token</button>
        </div>
        {token && <div className="enroll-token">
          <p>{token.reenroll ? "Re-enrollment" : "Enrollment"} token, valid until {formatDate(token.expiresAt)}. Copy it now; it cannot be shown again.</p>
          <pre className="remote-json">{token.value}</pre>
          <p className="muted">On the endpoint, save the token to a file readable only by the agent account, then run <code className="mono">nerve-agent enroll --api-url {typeof window === "undefined" ? "" : window.location.origin}/api/v1 --token-file &lt;file&gt;</code>{token.reenroll ? " with --force" : ""} and delete the file.</p>
          <button className="button" type="button" onClick={() => { void navigator.clipboard?.writeText(token.value); }}>Copy</button> <button className="button" type="button" onClick={() => setToken(null)}>Done</button>
        </div>}
        {enrollments.length > 0 && <div className="table-wrap"><table className="data-table"><thead><tr><th>Enrollment</th><th>State</th><th>Created by</th><th>Expires</th><th /></tr></thead><tbody>
          {enrollments.slice(0, 20).map((enrollment) => <tr key={enrollment.id}>
            <td>{enrollment.displayName ?? (enrollment.targetAgentId ? "Re-enrollment" : "—")}</td><td>{enrollment.state}</td>
            <td>{enrollment.createdByEmail ?? "—"}</td><td>{formatDate(enrollment.expiresAt)}</td>
            <td>{enrollment.state === "pending" && <button className="button" type="button" disabled={busy} onClick={() => void act(() => api.revokeEnrollment(enrollment.id))}>Revoke</button>}</td>
          </tr>)}
        </tbody></table></div>}
      </div>
    </div>}
  </section>;
}
