"use client";

import type { AuditRecord, RemoteCommand } from "@/lib/types";
import { activeStatuses, commandStatusDots, formatDate, statusLabels } from "./rmm-format";

// Command history, command results and the device audit trail of the RMM
// device page (#93).

export function CommandHistory({ commands, selected, canApprove, canExecute, onOpen, onReview, onCancel }: {
  commands: RemoteCommand[]; selected?: RemoteCommand | null; canApprove: boolean; canExecute: boolean;
  onOpen: (command: RemoteCommand) => void; onReview: (command: RemoteCommand) => void; onCancel: (command: RemoteCommand) => void;
}) {
  if (!commands.length) return <p className="rmm-note muted">No commands have been run on this device.</p>;
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>Command</th><th>Status</th><th>Requested by</th><th>Requested</th><th>Completed</th><th><span className="sr-only">Actions</span></th></tr></thead><tbody>
    {commands.map((command) => <tr key={command.id} aria-selected={selected?.id === command.id}>
      <td><button className="link-button" type="button" onClick={() => onOpen(command)}>{command.type}</button></td>
      <td><span className="device-status"><span className="status-dot" data-status={commandStatusDots[command.status]} aria-hidden="true" />{statusLabels[command.status]}{command.failureCode ? `, ${command.failureCode}` : ""}</span></td>
      <td>{command.requestedByEmail ?? "—"}</td><td className="mono">{formatDate(command.requestedAt)}</td><td className="mono">{formatDate(command.completedAt)}</td>
      <td className="rmm-row-actions">{command.status === "awaiting_approval" && canApprove && <button className="button button-primary button-small" type="button" onClick={() => onReview(command)}>Review and approve</button>}
        {activeStatuses.includes(command.status) && canExecute && <button className="button button-small" type="button" onClick={() => onCancel(command)}>Cancel</button>}</td>
    </tr>)}
  </tbody></table></div>;
}

export function CommandResult({ command }: { command: RemoteCommand }) {
  return <section className="remote-result" aria-label={`Result of ${command.type}`}>
    <div className="detail-section-heading"><h2>{command.type}: {statusLabels[command.status]}</h2></div>
    {command.failureReason && command.status !== "succeeded" && <p className="remote-note">{command.failureReason}</p>}
    {command.status === "awaiting_approval" && <p className="remote-note muted">Waiting for an operator to approve it.</p>}
    {activeStatuses.includes(command.status) && command.status !== "awaiting_approval" && <p className="remote-note muted">Waiting for the agent.</p>}
    {Object.keys(command.parameters ?? {}).length > 0 && <p className="remote-note muted">Parameters: <span className="mono">{JSON.stringify(command.parameters)}</span></p>}
    {command.approval && <p className="remote-note muted">Approved by {command.approval.approvedByEmail || "an operator"} on {formatDate(command.approval.approvedAt)}; the approval expires {formatDate(command.approval.expiresAt)}.</p>}
    {command.resultPruned && <p className="remote-note muted">The output was removed by the result retention policy.</p>}
    {command.result && <>
      <p className="remote-note muted">{command.result.outputBytes.toLocaleString()} bytes, received {formatDate(command.result.submittedAt)}{command.result.outputTruncated ? ", truncated to the output limit" : ""}</p>
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

export function ResultView({ output }: { output: unknown }) {
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

const auditResultDots: Record<string, string> = { success: "online", failure: "problem", denied: "warning" };
const targetLabels: Record<string, string> = { device: "Device", agent: "Agent", remote_command: "Command" };

function actor(record: AuditRecord) {
  if (record.actorEmail) return record.actorEmail;
  if (record.actorUserId) return "Deleted user";
  if (record.actorAgentId) return "Agent";
  return "NERVE (automatic)";
}

export function AuditTrail({ records, commands }: { records: AuditRecord[]; commands: RemoteCommand[] }) {
  if (!records.length) return <p className="rmm-note muted">No audit records exist for this device yet.</p>;
  const commandTypes = new Map(commands.map((command) => [command.id, command.type]));
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>Time</th><th>Action</th><th>Actor</th><th>Target</th><th>Result</th></tr></thead><tbody>
    {records.map((record) => <tr key={record.id}>
      <td className="mono">{formatDate(record.occurredAt)}</td>
      <td className="mono">{record.action}</td>
      <td>{actor(record)}</td>
      <td>{targetLabels[record.targetType] ?? record.targetType}{record.targetType === "remote_command" && record.targetId && commandTypes.has(record.targetId) ? ` ${commandTypes.get(record.targetId)}` : ""}</td>
      <td><span className="device-status"><span className="status-dot" data-status={auditResultDots[record.result] ?? "unknown"} aria-hidden="true" />{record.result}{record.errorCode ? `, ${record.errorCode}` : ""}</span></td>
    </tr>)}
  </tbody></table></div>;
}
