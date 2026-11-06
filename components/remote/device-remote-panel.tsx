"use client";

import { CircleOff, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { agentConnectionState } from "@/lib/agent-status";
import { APIError, api } from "@/lib/api";
import type { AuditRecord, RemoteCapability, RemoteCommand, RemoteEndpoint } from "@/lib/types";
import { ApprovalDialog } from "./approval-dialog";
import { MeshCentralEndpoint } from "./meshcentral-endpoint";
import { AuditTrail, CommandHistory, CommandResult } from "./rmm-activity";
import { activeStatuses, formatBytes, formatDate, formatDuration, statusLabels } from "./rmm-format";
import {
  ListSection, PerformanceSection, operatingSystem, SnapshotFrame, SystemSection, processColumns, serviceColumns, softwareColumns,
  type RowAction, type SnapshotSource,
} from "./rmm-inventory";

// The RMM view of one device (#93): observed inventory from the native agent,
// approval-gated system actions, command history, the device audit trail and
// MeshCentral presence. Every section names its source and shows its own
// loading, offline, denied and failed states.

type Section = "overview" | "system" | "performance" | "processes" | "services" | "software" | "actions" | "history" | "audit" | "session";
const sections: Array<{ id: Section; label: string }> = [
  { id: "overview", label: "Overview" }, { id: "system", label: "System" }, { id: "performance", label: "Performance" },
  { id: "processes", label: "Processes" }, { id: "services", label: "Services" }, { id: "software", label: "Software" },
  { id: "actions", label: "Actions" }, { id: "history", label: "Command history" }, { id: "audit", label: "Audit" },
  { id: "session", label: "Remote session" },
];
const snapshotTypes: Partial<Record<Section, { type: string; label: string }>> = {
  system: { type: "system.info", label: "System information" },
  performance: { type: "system.metrics", label: "Performance" },
  processes: { type: "process.list", label: "Processes" },
  services: { type: "service.list", label: "Services" },
  software: { type: "software.list", label: "Software" },
};
const associationLabels: Record<string, string> = {
  automatic: "Automatic (unique MAC match)", confirmed: "Confirmed by an operator",
};

// Parameters of the system-management commands (#92). Core and the privileged
// helper validate them again; these fields only collect them.
type ParameterField = { key: string; label: string; numeric?: boolean; optional?: boolean; hint: string };
const serviceField: ParameterField = { key: "name", label: "Service name", hint: "systemd unit or Windows service name, as in Services" };
const parameterFields: Record<string, ParameterField[]> = {
  "service.restart": [serviceField],
  "service.stop": [serviceField],
  "process.terminate": [
    { key: "pid", label: "PID", numeric: true, hint: "Process ID, as in Processes" },
    { key: "name", label: "Process name", hint: "Must match the running process" },
  ],
  "logs.read": [
    { key: "source", label: "Source (optional)", optional: true, hint: "Service or event source; empty for all" },
    { key: "lines", label: "Entries", numeric: true, optional: true, hint: "1–500, default 100" },
    { key: "sinceMinutes", label: "Minutes back", numeric: true, optional: true, hint: "1–10080, default 60" },
  ],
};
type ParameterForm = { endpoint: RemoteEndpoint; capability: RemoteCapability; values: Record<string, string> };

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export function DeviceRemotePanel({ deviceId, permissions }: { deviceId: string; permissions: string[] }) {
  const [endpoints, setEndpoints] = useState<RemoteEndpoint[] | null>(null);
  const [commands, setCommands] = useState<RemoteCommand[]>([]);
  const [snapshots, setSnapshots] = useState<RemoteCommand[] | null>(null);
  const [audit, setAudit] = useState<AuditRecord[] | null>(null);
  const [errors, setErrors] = useState<{ endpoints?: string; commands?: string; audit?: string }>({});
  const [actionError, setActionError] = useState<string | null>(null);
  const [selected, setSelected] = useState<RemoteCommand | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [section, setSection] = useState<Section>("overview");
  const [approving, setApproving] = useState<RemoteCommand | null>(null);
  const [form, setForm] = useState<ParameterForm | null>(null);
  const canView = permissions.includes("agents.view");
  const canSeeCommands = permissions.includes("remote.view");
  const canExecute = permissions.includes("remote.execute.read_only");
  const canApprove = permissions.includes("remote.execute.privileged");
  const canAudit = permissions.includes("audit.view");

  const load = useCallback(async () => {
    if (!canView) return;
    // Each source fails on its own, so one outage leaves the other sections usable.
    const [remote, history, latest, trail] = await Promise.allSettled([
      api.deviceRemote(deviceId),
      canSeeCommands ? api.deviceCommands(deviceId) : Promise.resolve({ items: [] as RemoteCommand[] }),
      canSeeCommands ? api.deviceSnapshots(deviceId) : Promise.resolve({ items: [] as RemoteCommand[] }),
      canAudit ? api.deviceAudit(deviceId) : Promise.resolve({ items: [] as AuditRecord[] }),
    ]);
    const next: typeof errors = {};
    if (remote.status === "fulfilled") setEndpoints(remote.value.endpoints);
    else next.endpoints = errorMessage(remote.reason, "Remote management state could not be loaded.");
    if (history.status === "fulfilled") setCommands(history.value.items);
    else next.commands = errorMessage(history.reason, "The command history could not be loaded.");
    if (latest.status === "fulfilled") setSnapshots(latest.value.items);
    else next.commands = next.commands ?? errorMessage(latest.reason, "The latest results could not be loaded.");
    if (trail.status === "fulfilled") setAudit(trail.value.items);
    else next.audit = errorMessage(trail.reason, "The audit log could not be loaded.");
    setErrors(next);
  }, [deviceId, canView, canSeeCommands, canAudit]);

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

  const agent = endpoints?.find((endpoint) => endpoint.provider === "nerve-agent");
  const meshNodes = endpoints?.filter((endpoint) => endpoint.provider === "meshcentral") ?? [];

  async function run(endpoint: RemoteEndpoint, type: string, parameters: Record<string, unknown> = {}) {
    setBusy(type); setActionError(null);
    try {
      const command = await api.createCommand(endpoint.endpointId, type, parameters);
      setForm(null);
      if (command.risk !== "read_only") setSelected(command);
      await load();
      // A privileged request goes straight to the approval step.
      if (command.status === "awaiting_approval" && canApprove) await review(command);
    } catch (requestError) {
      setActionError(requestError instanceof APIError ? requestError.message : "The command could not be queued.");
    } finally { setBusy(null); }
  }

  function request(endpoint: RemoteEndpoint, capability: RemoteCapability) {
    if (parameterFields[capability.name]) setForm({ endpoint, capability, values: {} });
    else void run(endpoint, capability.name);
  }

  function submitForm() {
    if (!form) return;
    // Empty optional fields are left out; Core fills in their defaults.
    const parameters = Object.fromEntries((parameterFields[form.capability.name] ?? []).flatMap((field) => {
      const value = (form.values[field.key] ?? "").trim();
      if (!value && field.optional) return [];
      return [[field.key, field.numeric ? Number(value) : value]];
    }));
    void run(form.endpoint, form.capability.name, parameters);
  }

  async function open(command: RemoteCommand) {
    try { setSelected(await api.command(command.id)); } catch { setActionError("The command could not be loaded."); }
  }

  async function cancel(command: RemoteCommand) {
    try { setSelected(await api.cancelCommand(command.id)); await load(); } catch (requestError) {
      setActionError(errorMessage(requestError, "The command could not be cancelled."));
    }
  }

  async function review(command: RemoteCommand) {
    // Load the detail: only it carries the target identity and evidence.
    try { setApproving(await api.command(command.id)); } catch { setActionError("The command could not be loaded."); }
  }

  async function approved(command: RemoteCommand) {
    setApproving(null); setSelected(command); await load();
  }

  if (!canView) return <Empty title="Not permitted" copy="Your role cannot view remote management." />;
  if (!endpoints) return errors.endpoints
    ? <div className="error-state"><h3>Remote management unavailable</h3><p>{errors.endpoints}</p><button className="button" type="button" onClick={() => void load()}>Retry</button></div>
    : <div className="skeleton-detail" aria-busy="true"><span /><span /><span /></div>;
  if (!endpoints.length) return <Empty title="No management agent" copy="No enrolled NERVE agent or MeshCentral node is associated with this device. Create an enrollment in Settings → Management agents, or confirm a candidate association there." />;

  const capability = (name: string) => agent?.capabilities.find((item) => item.name === name && item.executable);
  const privilegedActions = agent?.capabilities.filter((item) => item.executable && item.risk === "privileged") ?? [];
  const usable = agent?.status === "active";

  function source(type: string, label: string): SnapshotSource {
    return {
      type, label, capability: capability(type),
      snapshot: snapshots?.find((item) => item.type === type),
      latest: commands.find((item) => item.type === type),
      loading: canSeeCommands && snapshots === null && !errors.commands,
      failed: errors.commands,
      canRefresh: canExecute && usable,
      online: agent?.online ?? false,
      busy: busy !== null,
      onRefresh: () => { if (agent) void run(agent, type); },
    };
  }

  // Row actions only appear when the agent offers the command and the
  // operator may request it; the approval dialog shows the exact target.
  const rowAction = (type: string, label: string, parameters: RowAction["parameters"]): RowAction[] =>
    canApprove && usable && capability(type) ? [{ type, label, parameters }] : [];
  const processActions = rowAction("process.terminate", "End", (row) => typeof row.pid === "number" && typeof row.name === "string" ? { pid: row.pid, name: row.name } : null);
  const serviceName = (row: Record<string, unknown>) => typeof row.name === "string" ? { name: String(row.unit ?? row.name) } : null;
  const serviceActions = [...rowAction("service.restart", "Restart", serviceName), ...rowAction("service.stop", "Stop", serviceName)];
  const rowRun = (type: string, parameters: Record<string, unknown>) => { if (agent) void run(agent, type, parameters); };

  function body() {
    const snapshot = snapshotTypes[section];
    if (snapshot) {
      if (!agent) return <Empty title="No NERVE agent" copy="This section needs an enrolled NERVE agent on the device. MeshCentral nodes do not report inventory to NERVE." />;
      if (!canSeeCommands) return <Empty title="Not permitted" copy="Your role cannot view command results." />;
      const data = source(snapshot.type, snapshot.label);
      return <SnapshotFrame source={data}>{(output) => {
        switch (section) {
          case "system": return <SystemSection output={output} />;
          case "performance": return <PerformanceSection output={output} />;
          case "processes": return <ListSection output={output} columns={processColumns} noun="processes" singular="process" actions={processActions} busy={busy !== null} onAction={rowRun} />;
          case "services": return <ListSection output={output} columns={serviceColumns} noun="services" singular="service" actions={serviceActions} busy={busy !== null} onAction={rowRun} />;
          default: return <ListSection output={output} columns={softwareColumns} noun="packages" singular="package" />;
        }
      }}</SnapshotFrame>;
    }
    switch (section) {
      case "overview": return <Overview agent={agent} meshNodes={meshNodes} snapshots={snapshots} commands={commands} canSeeCommands={canSeeCommands} onOpen={setSection} />;
      case "actions": return actions();
      case "history": {
        if (!canSeeCommands) return <Empty title="Not permitted" copy="Your role cannot view the command history." />;
        if (errors.commands) return <Failed message={errors.commands} onRetry={load} />;
        return <><CommandHistory commands={commands} selected={selected} canApprove={canApprove} canExecute={canExecute}
          onOpen={(command) => void open(command)} onReview={(command) => void review(command)} onCancel={(command) => void cancel(command)} />
          {selected && <CommandResult command={selected} />}</>;
      }
      case "audit": {
        if (!canAudit) return <Empty title="Not permitted" copy="Your role cannot read the audit log." />;
        if (errors.audit) return <Failed message={errors.audit} onRetry={load} />;
        if (!audit) return <div className="skeleton-detail" aria-busy="true"><span /><span /><span /></div>;
        return <><p className="rmm-note muted">Records for this device, the agents associated with it now and its remote commands; newest 100.</p>
          <AuditTrail records={audit} commands={commands} /></>;
      }
      default: return meshNodes.length
        ? <>{meshNodes.map((endpoint) => <MeshCentralEndpoint key={endpoint.endpointId} deviceId={deviceId} endpoint={endpoint} canManage={permissions.includes("agents.manage")} onChanged={load} />)}
          <p className="rmm-note muted">Desktop, terminal and file sessions open in MeshCentral. Starting them from NERVE is planned with audited remote sessions.</p></>
        : <Empty title="No MeshCentral node" copy="Remote desktop, terminal and file sessions come from MeshCentral. Bind this device to a MeshCentral node in Settings to see its presence here." />;
    }
  }

  function actions() {
    if (!agent) return <Empty title="No NERVE agent" copy="System actions need an enrolled NERVE agent with the privileged helper." />;
    if (!usable) return <Empty title="Agent not active" copy="The agent is awaiting approval, revoked or removed; it cannot run commands." />;
    if (!canApprove) return <Empty title="Not permitted" copy="System actions need the privileged remote-execution permission and an approval." />;
    if (!privilegedActions.length) return <Empty title="No system actions" copy="The agent offers no system actions. Install it with the privileged helper to enable them." />;
    return <div>
      <p className="rmm-note muted">Each action needs an approval. The privileged helper on the device checks the approval again and refuses protected services and processes.
        {!agent.online && " The agent is offline; a request waits until it reconnects or expires."}</p>
      <div className="remote-actions">
        {privilegedActions.map((item) =>
          <button key={item.name} className="button" type="button" title={item.description} disabled={busy !== null} aria-busy={busy === item.name}
            aria-expanded={parameterFields[item.name] ? form?.capability.name === item.name : undefined}
            onClick={() => request(agent, item)}>{item.label}</button>)}
      </div>
      {form && <form className="remote-parameters" aria-label={form.capability.label + " parameters"}
        onSubmit={(event) => { event.preventDefault(); submitForm(); }}>
        {(parameterFields[form.capability.name] ?? []).map((field) => <label key={field.key} className="field"><span>{field.label}</span>
          <input className="input" required={!field.optional} inputMode={field.numeric ? "numeric" : undefined} pattern={field.numeric ? "[0-9]+" : undefined} title={field.hint}
            placeholder={field.hint} value={form.values[field.key] ?? ""}
            onChange={(event) => setForm({ ...form, values: { ...form.values, [field.key]: event.target.value } })} /></label>)}
        <button className="button button-primary" type="submit" disabled={busy !== null}>Request {form.capability.label.toLowerCase()}</button>
        <button className="button" type="button" onClick={() => setForm(null)}>Cancel</button>
      </form>}
      {selected && selected.risk !== "read_only" && <CommandResult command={selected} />}
    </div>;
  }

  const current = sections.find((item) => item.id === section)!;
  return <div className="rmm-layout">
    <nav className="rmm-nav" aria-label="Remote management sections">
      {sections.map((item) => <button key={item.id} type="button" aria-current={section === item.id ? "true" : undefined}
        onClick={() => setSection(item.id)}>{item.label}</button>)}
    </nav>
    <div className="rmm-body">
      <div className="detail-section-heading remote-heading"><h2>{current.label}</h2>
        <button className="button" type="button" onClick={() => void load()}><RefreshCw size={14} aria-hidden="true" />Reload</button></div>
      {actionError && <div className="form-error" role="alert">{actionError}</div>}
      {body()}
    </div>
    {approving && <ApprovalDialog command={approving} onClose={() => setApproving(null)} onApproved={(command) => void approved(command)} />}
  </div>;
}

function Overview({ agent, meshNodes, snapshots, commands, canSeeCommands, onOpen }: {
  agent?: RemoteEndpoint; meshNodes: RemoteEndpoint[]; snapshots: RemoteCommand[] | null; commands: RemoteCommand[];
  canSeeCommands: boolean; onOpen: (section: Section) => void;
}) {
  const output = (type: string) => {
    const value = snapshots?.find((item) => item.type === type)?.result?.output;
    return typeof value === "object" && value !== null ? value as Record<string, unknown> : undefined;
  };
  const info = output("system.info"), metrics = output("system.metrics");
  const cpu = metrics?.cpu as Record<string, unknown> | undefined, memory = metrics?.memory as Record<string, unknown> | undefined;
  const metricsAt = snapshots?.find((item) => item.type === "system.metrics")?.result?.submittedAt;
  const awaiting = commands.filter((command) => command.status === "awaiting_approval").length;
  const lastCommand = commands[0];
  const state = agent ? agentConnectionState(agent.status, agent.connection, agent.online) : undefined;
  return <div>
    <dl className="property-list">
      <div><dt>NERVE agent</dt><dd>{agent && state
        ? <span className="device-status"><span className="status-dot" data-status={state.dot} aria-hidden="true" />{state.label}, {agent.displayName}</span>
        : <span className="device-status"><span className="status-dot" data-status="unknown" aria-hidden="true" />Not enrolled</span>}</dd></div>
      {agent && <>
        <div><dt>Agent version</dt><dd className="mono">{agent.version || "—"}</dd></div>
        <div><dt>Last heartbeat</dt><dd className="mono">{formatDate(agent.lastSeenAt)}</dd></div>
        <div><dt>Association</dt><dd>{associationLabels[agent.association] ?? agent.association}</dd></div>
        <div><dt>Privileged helper</dt><dd>{!agent.capabilities.some((item) => item.risk === "privileged" && item.executable)
          ? "Not reported: read-only commands only"
          : agent.transport === "https" ? "Installed: system actions available"
          : "Installed, but system actions are refused until the agent connects over verified HTTPS"}</dd></div>
      </>}
      <div><dt>MeshCentral</dt><dd>{meshNodes.length
        ? meshNodes.map((node) => `${node.displayName}: ${node.status === "absent" ? "not in the latest listing" : node.online ? "MeshAgent connected" : "MeshAgent not connected"}`).join("; ")
        : "No node bound"}</dd></div>
    </dl>
    {canSeeCommands && <>
      <div className="detail-section-heading"><h2>Last reports</h2></div>
      <dl className="property-list">
        <div><dt>Operating system</dt><dd>{info ? operatingSystem(info) : <EmptyValue onOpen={() => onOpen("system")} />}</dd></div>
        <div><dt>Uptime</dt><dd>{typeof info?.uptimeSeconds === "number" ? formatDuration(info.uptimeSeconds) : info ? "Not reported" : <EmptyValue onOpen={() => onOpen("system")} />}</dd></div>
        <div><dt>CPU and memory</dt><dd>{metrics
          ? <>{typeof cpu?.usagePercent === "number" ? `CPU ${cpu.usagePercent.toFixed(1)} %` : "CPU not reported"}, {typeof memory?.usedBytes === "number" && typeof memory?.totalBytes === "number"
            ? `memory ${formatBytes(memory.usedBytes)} of ${formatBytes(memory.totalBytes)}` : "memory not reported"} <span className="muted">(at <time className="mono" dateTime={metricsAt}>{formatDate(metricsAt)}</time>)</span></>
          : <EmptyValue onOpen={() => onOpen("performance")} />}</dd></div>
        <div><dt>Awaiting approval</dt><dd>{awaiting ? <button className="link-button" type="button" onClick={() => onOpen("history")}>{awaiting} command{awaiting === 1 ? "" : "s"}</button> : "None"}</dd></div>
        <div><dt>Last command</dt><dd>{lastCommand ? <button className="link-button" type="button" onClick={() => onOpen("history")}>{lastCommand.type}: {statusLabels[lastCommand.status]} on {formatDate(lastCommand.requestedAt)}</button> : "None yet"}</dd></div>
      </dl>
    </>}
  </div>;
}

function EmptyValue({ onOpen }: { onOpen: () => void }) {
  return <span className="muted">Not collected yet. <button className="link-button" type="button" onClick={onOpen}>Request it</button></span>;
}

function Failed({ message, onRetry }: { message: string; onRetry: () => Promise<void> }) {
  return <div className="rmm-state" role="alert"><span className="status-dot" data-status="problem" aria-hidden="true" />
    <div><strong>Could not be loaded</strong><p>{message}</p><button className="button" type="button" onClick={() => void onRetry()}>Retry</button></div></div>;
}

function Empty({ title, copy }: { title: string; copy: string }) {
  return <div className="empty-state detail-empty"><CircleOff /><h2>{title}</h2><p>{copy}</p></div>;
}
