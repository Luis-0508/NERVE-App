"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { agentConnectionState, runModeLabel } from "@/lib/agent-status";
import type { AgentDeploymentKey, AgentEnrollment, ManagementAgent } from "@/lib/types";

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function runtimeTitle(agent: ManagementAgent) {
  const parts = [];
  if (agent.processStartedAt) parts.push(`Agent started ${formatDate(agent.processStartedAt)}`);
  if (agent.hostBootAt) parts.push(`host booted ${formatDate(agent.hostBootAt)}`);
  if (agent.lastShutdownAt) parts.push(`last clean stop ${formatDate(agent.lastShutdownAt)}`);
  return parts.join(", ") || undefined;
}

// A single-quoted PowerShell literal expands nothing; only ' itself needs doubling.
function psQuote(value: string) {
  return `'${value.replace(/'/g, "''")}'`;
}

// The token reaches the agent through NERVE_AGENT_ENROLLMENT_TOKEN, never a process
// argument, and the variable is cleared from the session even when enrollment fails.
function powershellEnrollCommand(token: string, apiBase: string, reenroll: boolean) {
  const run = reenroll
    ? `& (Join-Path $env:ProgramFiles 'NERVE\\Agent\\nerve-agent.exe') enroll --force; if ($LASTEXITCODE -ne 0) { throw "nerve-agent enroll failed with exit code $LASTEXITCODE." }`
    : `if (-not (Test-Path -LiteralPath .\\install-windows.ps1)) { throw 'install-windows.ps1 was not found in this folder. Change to the folder that contains install-windows.ps1 and nerve-agent.exe, then paste the command again.' }; .\\install-windows.ps1 -ApiUrl ${psQuote(apiBase)}`;
  return `$env:NERVE_AGENT_ENROLLMENT_TOKEN = ${psQuote(token)}; try { ${run} } finally { Remove-Item Env:\\NERVE_AGENT_ENROLLMENT_TOKEN -ErrorAction SilentlyContinue }`;
}

export function AgentsSettings({ permissions }: { permissions: string[] }) {
  const canManage = permissions.includes("agents.manage");
  const [agents, setAgents] = useState<ManagementAgent[] | null>(null);
  const [enrollments, setEnrollments] = useState<AgentEnrollment[]>([]);
  const [deploymentKeys, setDeploymentKeys] = useState<AgentDeploymentKey[]>([]);
  const [token, setToken] = useState<{ value: string; expiresAt: string; reenroll: boolean } | null>(null);
  const [newKey, setNewKey] = useState<{ value: string; key: AgentDeploymentKey } | null>(null);
  const [name, setName] = useState("");
  const [minutes, setMinutes] = useState(60);
  const [keyName, setKeyName] = useState("");
  const [keyUses, setKeyUses] = useState(25);
  const [keyDays, setKeyDays] = useState(7);
  const [keyApproval, setKeyApproval] = useState<"manual" | "automatic">("manual");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState<string | null>(null);
  const apiBase = `${typeof window === "undefined" ? "" : window.location.origin}/api/v1`;

  const load = useCallback(async () => {
    try {
      const [agentList, enrollmentList, keyList] = await Promise.all([api.agents(),
        canManage ? api.enrollments() : Promise.resolve({ items: [] }), canManage ? api.deploymentKeys() : Promise.resolve({ items: [] })]);
      setAgents(agentList.items); setEnrollments(enrollmentList.items); setDeploymentKeys(keyList.items); setError(null);
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
      setName(""); setCopied(null);
    });
  }

  async function copy(label: string, value: string) {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(`${label} copied.`);
    } catch {
      setCopied("Copying failed; select the text and copy it manually.");
    }
  }

  async function createDeploymentKey() {
    await act(async () => {
      const result = await api.createDeploymentKey({ name: keyName.trim(), approval: keyApproval, maxUses: keyUses, expiresInDays: keyDays });
      setNewKey({ value: result.key, key: result.deploymentKey });
      setKeyName("");
    });
  }

  const pendingCount = agents?.filter((agent) => agent.status === "pending").length ?? 0;
  const keyNames = new Map(deploymentKeys.map((key) => [key.id, key.name]));

  return <section className="settings-panel agents-settings">
    <div className="settings-row"><div><h2>Management agents</h2><p>Enrolled NERVE agents. Each has its own Ed25519 key; revoking one never affects another.</p></div>
      <div>{error && <div className="form-error" role="alert">{error}</div>}
        {canManage && pendingCount > 0 && <p className="agents-pending" role="status"><span className="status-dot" data-status="warning" aria-hidden="true" />
          {pendingCount === 1 ? "1 agent awaits approval." : `${pendingCount} agents await approval.`} Compare the host and key fingerprint with the endpoint before approving; pending agents receive no commands.</p>}
        {agents === null ? <p className="muted">{error ? "" : "Loading agents…"}</p> : agents.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>Agent</th><th>Status</th><th>Host</th><th>Device</th><th>Version</th><th>Last seen</th><th>Key</th>{canManage && <th />}</tr></thead><tbody>
          {agents.map((agent) => {
            const state = agentConnectionState(agent.status, agent.connection, agent.online);
            const runMode = runModeLabel(agent.runMode);
            const candidate = agent.association.status === "candidate" && agent.association.evidence.length === 1 ? agent.association.evidence[0] : null;
            const pending = agent.status === "pending";
            return <tr key={agent.id}>
              <td>{agent.displayName}{agent.deploymentKeyId && <small className="device-alias">via key {keyNames.get(agent.deploymentKeyId) ?? agent.deploymentKeyId.slice(0, 8)}</small>}</td>
              <td><span className="device-status"><span className="status-dot" data-status={state.dot} aria-hidden="true" />{state.label}</span></td>
              <td>{agent.hostname ?? "—"}<span className="muted"> · {agent.os ?? "?"}/{agent.architecture ?? "?"}</span></td>
              <td>{agent.device ? <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(agent.device.id)}`}>{agent.device.name}</Link> : <span className="muted">{agent.association.status}</span>}
                {agent.device && <span className="muted"> · {agent.association.status}</span>}
                {candidate && <span className="muted" title={candidate.signals.map((signal) => `${signal.type}: ${signal.value}`).join(", ")}> · candidate {candidate.deviceName}</span>}</td>
              <td title={runtimeTitle(agent)}><span className="mono">{agent.agentVersion ?? "—"}</span>{runMode && <span className="muted"> · {runMode}</span>}</td>
              <td>{formatDate(agent.lastSeenAt)}</td>
              <td className="mono" title={agent.credential?.fingerprint}>{agent.credential ? agent.credential.fingerprint.slice(0, 12) : "—"}</td>
              {canManage && pending && <td className="agent-actions">
                <button className="button button-primary" type="button" disabled={busy} onClick={() => void act(() => api.approveAgent(agent.id))}>Approve</button>
                <button className="button" type="button" disabled={busy}
                  onClick={() => { if (window.confirm(`Reject ${agent.displayName}? The agent is removed and its key stops working.`)) void act(() => api.removeAgent(agent.id)); }}>Reject</button>
              </td>}
              {canManage && !pending && <td className="agent-actions">
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
          <p className="muted">{token.reenroll
            ? "Windows: paste into an Administrator PowerShell on the endpoint. A running service switches to the new key within 15 seconds."
            : "Windows: paste into an Administrator PowerShell in the folder that contains install-windows.ps1 and nerve-agent.exe."}{" "}
            The token is already part of the command and is handed to the agent through an environment variable, not a file. The pasted line may stay in the PowerShell history; the token is single-use and expires, so revoke it below if enrollment does not finish.</p>
          <pre className="remote-json">{powershellEnrollCommand(token.value, apiBase, token.reenroll)}</pre>
          <p className="muted">Linux or manual setup: save the token to a file only administrators can read, then run {token.reenroll
            ? <code className="mono">sudo nerve-agent enroll --force --token-file &lt;file&gt;</code>
            : <code className="mono">sudo ./install-linux.sh --api-url {apiBase} --token-file &lt;file&gt;</code>} and delete the file.</p>
          <button className="button button-primary" type="button" onClick={() => void copy("PowerShell command", powershellEnrollCommand(token.value, apiBase, token.reenroll))}>Copy PowerShell command</button>{" "}
          <button className="button" type="button" onClick={() => void copy("Token", token.value)}>Copy token</button>{" "}
          <button className="button" type="button" onClick={() => setToken(null)}>Done</button>
          {copied && <span className="muted enroll-copied" role="status">{copied}</span>}
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

    {canManage && <div className="settings-row"><div><h2>Deployment keys</h2><p>One key enrolls many endpoints, for example in a GPO or Intune rollout. It is limited by uses and lifetime and can be revoked; agents it already enrolled keep working.</p></div>
      <div>
        <div className="enroll-form">
          <label className="field"><span>Name</span><input className="input" value={keyName} maxLength={200} placeholder="Office PCs rollout" onChange={(event) => setKeyName(event.target.value)} /></label>
          <label className="field"><span>Maximum uses</span><input className="input" type="number" min={1} max={10000} value={keyUses} onChange={(event) => setKeyUses(Number(event.target.value))} /></label>
          <label className="field"><span>Valid for (days)</span><input className="input" type="number" min={1} max={90} value={keyDays} onChange={(event) => setKeyDays(Number(event.target.value))} /></label>
          <label className="field"><span>New agents</span><select className="input" value={keyApproval} onChange={(event) => setKeyApproval(event.target.value as "manual" | "automatic")}>
            <option value="manual">Wait for approval</option><option value="automatic">Activate automatically</option></select></label>
          <button className="button button-primary" type="button" disabled={busy || !keyName.trim()} onClick={() => void createDeploymentKey()}>Create key</button>
        </div>
        {keyApproval === "automatic" && <p className="muted">With automatic activation, anyone who obtains the key can add an active agent until the key is revoked, expires or runs out of uses.</p>}
        {newKey && <div className="enroll-token">
          <p>Deployment key “{newKey.key.name}”, {newKey.key.maxUses} uses, valid until {formatDate(newKey.key.expiresAt)}. Copy it now; it cannot be shown again.</p>
          <pre className="remote-json">{newKey.value}</pre>
          <p className="muted">Use it like an enrollment token on every endpoint, for example <code className="mono">.\install-windows.ps1 -ApiUrl {apiBase} -TokenFile &lt;file&gt;</code> or <code className="mono">sudo ./install-linux.sh --api-url {apiBase} --token-file &lt;file&gt;</code>. Anyone with the key can enroll endpoints, so store it like a password.</p>
          <button className="button" type="button" onClick={() => { void navigator.clipboard?.writeText(newKey.value); }}>Copy</button> <button className="button" type="button" onClick={() => setNewKey(null)}>Done</button>
        </div>}
        {deploymentKeys.length > 0 && <div className="table-wrap"><table className="data-table"><thead><tr><th>Key</th><th>State</th><th>Uses</th><th>New agents</th><th>Expires</th><th>Created by</th><th /></tr></thead><tbody>
          {deploymentKeys.slice(0, 20).map((key) => <tr key={key.id}>
            <td>{key.name}</td><td>{key.state}</td><td className="mono">{key.usedCount} / {key.maxUses}</td>
            <td>{key.approval === "manual" ? "Wait for approval" : "Activate automatically"}</td>
            <td>{formatDate(key.expiresAt)}</td><td>{key.createdByEmail ?? "—"}</td>
            <td>{key.state !== "revoked" && key.state !== "expired" && <button className="button" type="button" disabled={busy}
              onClick={() => { if (window.confirm(`Revoke deployment key ${key.name}? No further endpoints can enroll with it.`)) void act(() => api.revokeDeploymentKey(key.id)); }}>Revoke</button>}</td>
          </tr>)}
        </tbody></table></div>}
      </div>
    </div>}
  </section>;
}
