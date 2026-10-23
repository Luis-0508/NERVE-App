"use client";

import { CircleOff, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { agentConnectionState } from "@/lib/agent-status";
import { APIError, api } from "@/lib/api";
import type { RemoteCommand, RemoteCommandStatus, RemoteEndpoint } from "@/lib/types";

const activeStatuses: RemoteCommandStatus[] = ["queued", "claimed", "running"];
const statusLabels: Record<RemoteCommandStatus, string> = {
  queued: "Queued", claimed: "Claimed", running: "Running", succeeded: "Succeeded",
  failed: "Failed", timed_out: "Timed out", cancelled: "Cancelled", expired: "Expired",
};
const associationLabels: Record<string, string> = {
  automatic: "Automatic (unique MAC match)", confirmed: "Confirmed by an operator",
};

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(new Date(value));
}

export function DeviceRemotePanel({ deviceId, permissions }: { deviceId: string; permissions: string[] }) {
  const [endpoints, setEndpoints] = useState<RemoteEndpoint[] | null>(null);
  const [commands, setCommands] = useState<RemoteCommand[]>([]);
  const [selected, setSelected] = useState<RemoteCommand | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const canView = permissions.includes("agents.view");
  const canSeeCommands = permissions.includes("remote.view");
  const canExecute = permissions.includes("remote.execute.read_only");

  const load = useCallback(async () => {
    if (!canView) return;
    try {
      const [remote, history] = await Promise.all([
        api.deviceRemote(deviceId),
        canSeeCommands ? api.deviceCommands(deviceId) : Promise.resolve({ items: [] as RemoteCommand[] }),
      ]);
      setEndpoints(remote.endpoints);
      setCommands(history.items);
      setError(null);
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "Remote management state could not be loaded.");
    }
  }, [deviceId, canView, canSeeCommands]);

  useEffect(() => { void load(); }, [load]);

  const pending = useMemo(() => commands.some((command) => activeStatuses.includes(command.status)), [commands]);
  useEffect(() => {
    if (!pending) return;
    const timer = window.setInterval(() => { void load(); }, 2000);
    return () => window.clearInterval(timer);
  }, [pending, load]);

  // Keep the open result in sync with the polled history.
  useEffect(() => {
    if (!selected || !canSeeCommands) return;
    const latest = commands.find((command) => command.id === selected.id);
    if (latest && latest.status !== selected.status) void api.command(selected.id).then(setSelected).catch(() => undefined);
  }, [commands, selected, canSeeCommands]);

  async function run(endpoint: RemoteEndpoint, type: string) {
    setBusy(type); setError(null);
    try {
      const command = await api.createCommand(endpoint.endpointId, type);
      setSelected(command);
      await load();
    } catch (requestError) {
      setError(requestError instanceof APIError ? requestError.message : "The command could not be queued.");
    } finally { setBusy(null); }
  }

  async function open(command: RemoteCommand) {
    try { setSelected(await api.command(command.id)); } catch { setError("The command could not be loaded."); }
  }

  async function cancel(command: RemoteCommand) {
    try { setSelected(await api.cancelCommand(command.id)); await load(); } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The command could not be cancelled.");
    }
  }

  if (!canView) return <Empty title="Not permitted" copy="Your role cannot view remote management." />;
  if (!endpoints) return error ? <div className="error-state"><h3>Remote management unavailable</h3><p>{error}</p></div> : <div className="detail-empty" />;
  if (!endpoints.length) return <Empty title="No management agent" copy="No enrolled NERVE agent is associated with this device. Create an enrollment in Settings → Management agents, or confirm a candidate association there." />;

  return <div className="remote-panel">
    {error && <div className="form-error" role="alert">{error}</div>}
    {endpoints.map((endpoint) => <section key={endpoint.endpointId} className="remote-endpoint">
      <div className="detail-section-heading remote-heading"><h2>{endpoint.provider === "nerve-agent" ? "NERVE agent" : endpoint.provider} · {endpoint.displayName}</h2>
        <button className="button" type="button" onClick={() => void load()}><RefreshCw size={14} aria-hidden="true" />Refresh</button></div>
      <dl className="property-list">
        <div><dt>Agent</dt><dd>{(() => {
          const state = agentConnectionState(endpoint.status, endpoint.connection, endpoint.online);
          return <span className="device-status"><span className="status-dot" data-status={state.dot} aria-hidden="true" />{state.label}</span>;
        })()}</dd></div>
        <div><dt>Version</dt><dd className="mono">{endpoint.version || "—"}</dd></div>
        <div><dt>Last seen</dt><dd>{formatDate(endpoint.lastSeenAt)}</dd></div>
        <div><dt>Association</dt><dd>{associationLabels[endpoint.association] ?? endpoint.association}</dd></div>
        <div><dt>Capabilities</dt><dd>{endpoint.capabilities.length ? endpoint.capabilities.map((capability) => capability.label).join(", ") : "None reported"}</dd></div>
      </dl>
      {canExecute && endpoint.status === "active" && <div className="remote-actions">
        {endpoint.capabilities.filter((capability) => capability.executable).map((capability) =>
          <button key={capability.name} className="button" type="button" title={capability.description} disabled={busy !== null} aria-busy={busy === capability.name}
            onClick={() => void run(endpoint, capability.name)}>{capability.label}</button>)}
        {!endpoint.online && <span className="muted">The agent is offline; commands wait until it reconnects or expire.</span>}
      </div>}
    </section>)}

    {canSeeCommands && <section>
      <div className="detail-section-heading"><h2>Command history</h2></div>
      {commands.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>Command</th><th>Status</th><th>Requested by</th><th>Requested</th><th>Completed</th><th /></tr></thead><tbody>
        {commands.map((command) => <tr key={command.id} aria-selected={selected?.id === command.id}>
          <td><button className="link-button" type="button" onClick={() => void open(command)}>{command.type}</button></td>
          <td>{statusLabels[command.status]}{command.failureCode ? ` · ${command.failureCode}` : ""}</td>
          <td>{command.requestedByEmail ?? "—"}</td><td>{formatDate(command.requestedAt)}</td><td>{formatDate(command.completedAt)}</td>
          <td>{activeStatuses.includes(command.status) && canExecute && <button className="button" type="button" onClick={() => void cancel(command)}>Cancel</button>}</td>
        </tr>)}
      </tbody></table></div> : <p className="remote-note muted">No commands have been run on this device.</p>}
    </section>}

    {selected && <CommandResult command={selected} />}
  </div>;
}

function CommandResult({ command }: { command: RemoteCommand }) {
  return <section className="remote-result">
    <div className="detail-section-heading"><h2>{command.type} · {statusLabels[command.status]}</h2></div>
    {command.failureReason && command.status !== "succeeded" && <p className="remote-note">{command.failureReason}</p>}
    {activeStatuses.includes(command.status) && <p className="remote-note muted">Waiting for the agent…</p>}
    {command.resultPruned && <p className="remote-note muted">The output was removed by the result retention policy.</p>}
    {command.result && <>
      <p className="remote-note muted">{command.result.outputBytes.toLocaleString()} bytes · received {formatDate(command.result.submittedAt)}{command.result.outputTruncated ? " · truncated to the output limit" : ""}</p>
      {command.result.redactions && Object.keys(command.result.redactions).length > 0 && <p className="remote-note muted">Secrets removed before storage: {Object.entries(command.result.redactions).map(([kind, count]) => `${count} × ${kind.replaceAll("_", " ")}`).join(", ")}.</p>}
      {command.result.outputWithheld ? <p className="remote-note muted">The output of privileged commands is shown only to operators allowed to run them.</p> : <ResultView output={command.result.output} />}
    </>}
  </section>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function display(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

function ResultView({ output }: { output: unknown }) {
  if (!isRecord(output)) return <pre className="remote-json">{JSON.stringify(output, null, 2)}</pre>;
  const items = Array.isArray(output.items) ? output.items.filter(isRecord) : null;
  const scalars = Object.entries(output).filter(([key, value]) => key !== "items" && !Array.isArray(value) && !isRecord(value));
  const nested = Object.entries(output).filter(([key, value]) => key !== "items" && (Array.isArray(value) || isRecord(value)));
  const columns = items?.length ? Array.from(new Set(items.slice(0, 50).flatMap((item) => Object.keys(item)))).slice(0, 8) : [];
  return <div>
    <dl className="property-list">{scalars.map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="mono">{display(value)}</dd></div>)}</dl>
    {nested.map(([key, value]) => <div key={key}><div className="detail-section-heading"><h2>{key}</h2></div><pre className="remote-json">{JSON.stringify(value, null, 2)}</pre></div>)}
    {items && <div className="table-wrap"><table className="data-table"><thead><tr>{columns.map((column) => <th key={column}>{column}</th>)}</tr></thead><tbody>
      {items.slice(0, 500).map((item, index) => <tr key={index}>{columns.map((column) => <td key={column} className={typeof item[column] === "number" ? "mono" : undefined}>{display(item[column])}</td>)}</tr>)}
    </tbody></table>{items.length > 500 && <p className="remote-note muted">Showing 500 of {items.length} rows.</p>}</div>}
  </div>;
}

function Empty({ title, copy }: { title: string; copy: string }) {
  return <div className="empty-state detail-empty"><CircleOff /><h2>{title}</h2><p>{copy}</p></div>;
}
