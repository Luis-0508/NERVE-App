"use client";

import { RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import type { RemoteCapability, RemoteCommand } from "@/lib/types";
import { activeStatuses, formatBytes, formatDate, formatDuration, statusLabels } from "./rmm-format";

// The inventory sections of the RMM device page (#93). Each shows the newest
// retained result of one read-only command exactly as the agent reported it,
// with its time and requester; nothing is estimated or filled in.

export type SnapshotSource = {
  type: string;
  label: string;
  capability?: RemoteCapability;
  snapshot?: RemoteCommand;
  // The newest command of this type, which may be newer than the snapshot.
  latest?: RemoteCommand;
  loading: boolean;
  failed?: string;
  canRefresh: boolean;
  online: boolean;
  busy: boolean;
  onRefresh: () => void;
};

type Row = Record<string, unknown>;

export type RowAction = { label: string; type: string; parameters: (row: Row) => Record<string, unknown> | null };

function record(value: unknown): Row | null {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Row : null;
}

function rows(output: unknown): Row[] {
  const items = record(output)?.items;
  return Array.isArray(items) ? items.map(record).filter((item): item is Row => item !== null) : [];
}

function text(value: unknown) {
  return value === undefined || value === null || value === "" ? "—" : String(value);
}

function num(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

// SnapshotFrame renders the provenance line and the loading, unsupported,
// empty, refreshing, failed and offline states shared by every section.
export function SnapshotFrame({ source, children }: { source: SnapshotSource; children: (output: unknown) => React.ReactNode }) {
  const { snapshot, latest } = source;
  if (source.loading) return <div className="skeleton-detail" aria-busy="true"><span /><span /><span /></div>;
  if (source.failed) return <div className="rmm-state" role="alert"><span className="status-dot" data-status="problem" aria-hidden="true" />
    <div><strong>{source.label} could not be loaded</strong><p>{source.failed}</p></div></div>;
  if (!source.capability) return <div className="rmm-state"><span className="status-dot" data-status="unknown" aria-hidden="true" />
    <div><strong>Not reported by this agent</strong><p>The agent does not offer {source.label.toLowerCase()}. Its operating system or version may not support it.</p></div></div>;

  const refreshing = latest && activeStatuses.includes(latest.status) ? latest : undefined;
  const failedLatest = latest && ["failed", "timed_out", "expired"].includes(latest.status) && (!snapshot || latest.requestedAt > snapshot.requestedAt) ? latest : undefined;
  const refresh = source.canRefresh && <button className="button" type="button" disabled={source.busy || Boolean(refreshing)} aria-busy={source.busy}
    onClick={source.onRefresh}><RefreshCw size={14} aria-hidden="true" />{snapshot ? "Refresh" : "Request now"}</button>;

  return <div>
    <div className="rmm-provenance">
      <p>{snapshot?.result
        ? <>Reported by the agent on <time className="mono" dateTime={snapshot.result.submittedAt}>{formatDate(snapshot.result.submittedAt)}</time>{snapshot.requestedByEmail ? `, requested by ${snapshot.requestedByEmail}` : ""}.</>
        : "No result has been collected yet."}</p>
      {refresh}
    </div>
    {refreshing && <p className="rmm-note"><span className="status-dot" data-status="info" aria-hidden="true" />{statusLabels[refreshing.status]}: a newer result is on its way.</p>}
    {failedLatest && <p className="rmm-note"><span className="status-dot" data-status="warning" aria-hidden="true" />
      The last request {statusLabels[failedLatest.status].toLowerCase()} on {formatDate(failedLatest.completedAt ?? failedLatest.requestedAt)}{failedLatest.failureReason ? `: ${failedLatest.failureReason}` : "."}{snapshot ? " The data below is older." : ""}</p>}
    {!source.online && <p className="rmm-note"><span className="status-dot" data-status="offline" aria-hidden="true" />
      The agent is offline. {snapshot ? "The data below is the last report; a" : "A"} request waits until it reconnects or expires.</p>}
    {!source.canRefresh && !snapshot && <p className="rmm-note muted">Your role cannot request new data.</p>}
    {snapshot?.result?.outputTruncated && <p className="rmm-note"><span className="status-dot" data-status="limited" aria-hidden="true" />The report exceeded the output limit and is incomplete.</p>}
    {snapshot?.result && children(snapshot.result.output)}
  </div>;
}

export function SystemSection({ output }: { output: unknown }) {
  const info = record(output) ?? {};
  const uptime = num(info.uptimeSeconds);
  return <dl className="property-list">
    <Property label="Hostname" value={text(info.hostname)} mono />
    <Property label="Operating system" value={operatingSystem(info)} />
    <Property label="Kernel" value={text(info.kernelVersion)} mono />
    <Property label="Architecture" value={text(info.architecture)} mono />
    <Property label="Logical CPUs" value={text(info.logicalCpus)} />
    <Property label="Uptime" value={uptime === undefined ? "Not reported" : formatDuration(uptime)} />
    <Property label="Booted" value={typeof info.bootTime === "string" ? formatDate(info.bootTime) : "Not reported"} />
    <Property label="Agent version" value={text(info.agentVersion)} mono />
  </dl>;
}

// usageState maps a usage percentage to a status: colour carries state only.
function usageState(percent: number) {
  if (percent >= 90) return { status: "problem", label: "Critical" };
  if (percent >= 80) return { status: "warning", label: "High" };
  return { status: "neutral", label: "" };
}

export function Meter({ label, percent, detail }: { label: string; percent?: number; detail: string }) {
  if (percent === undefined) return <div className="rmm-meter"><div className="rmm-meter-label"><span>{label}</span><span className="muted">Not reported</span></div></div>;
  const state = usageState(percent);
  const clamped = Math.max(0, Math.min(100, percent));
  return <div className="rmm-meter">
    <div className="rmm-meter-label"><span>{label}</span><span>
      {state.label && <><span className="status-dot" data-status={state.status} aria-hidden="true" />{state.label} </>}
      <strong className="mono">{percent.toFixed(1)} %</strong></span></div>
    <div className="rmm-meter-track" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={clamped} aria-valuetext={`${percent.toFixed(1)} percent, ${detail}`}>
      <span data-status={state.status} style={{ width: `${clamped}%` }} /></div>
    <small className="muted">{detail}</small>
  </div>;
}

export function PerformanceSection({ output }: { output: unknown }) {
  const metrics = record(output) ?? {};
  const cpu = record(metrics.cpu) ?? {};
  const memory = record(metrics.memory) ?? {};
  const disks = Array.isArray(metrics.disks) ? metrics.disks.map(record).filter((disk): disk is Row => disk !== null) : [];
  const memoryUsed = num(memory.usedBytes), memoryTotal = num(memory.totalBytes);
  return <div className="rmm-meters">
    <Meter label="CPU" percent={num(cpu.usagePercent)} detail={`${text(cpu.logicalCores)} logical cores, sampled over ${text(cpu.sampleMilliseconds)} ms`} />
    <Meter label="Memory" percent={num(memory.usagePercent)} detail={memoryUsed !== undefined && memoryTotal !== undefined ? `${formatBytes(memoryUsed)} of ${formatBytes(memoryTotal)} in use` : "Size not reported"} />
    {disks.length ? disks.map((disk, index) => {
      const used = num(disk.usedBytes), total = num(disk.totalBytes);
      return <Meter key={`${text(disk.mountPoint)}-${index}`} label={`Disk ${text(disk.mountPoint)}`} percent={num(disk.usagePercent)}
        detail={`${used !== undefined && total !== undefined ? `${formatBytes(used)} of ${formatBytes(total)} used` : "Size not reported"}${disk.fileSystem ? `, ${String(disk.fileSystem)}` : ""}`} />;
    }) : <p className="rmm-note muted">No disks were reported.</p>}
  </div>;
}

type Column = { key: string; label: string; render?: (row: Row) => React.ReactNode; numeric?: boolean };

// ListSection is a filterable table for processes, services and software.
export function ListSection({ output, columns, noun, singular, actions = [], busy, onAction }: {
  output: unknown; columns: Column[]; noun: string; singular: string; actions?: RowAction[]; busy?: boolean;
  onAction?: (type: string, parameters: Record<string, unknown>) => void;
}) {
  const [query, setQuery] = useState("");
  const all = useMemo(() => rows(output), [output]);
  const meta = record(output) ?? {};
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return all;
    return all.filter((row) => columns.some((column) => String(row[column.key] ?? "").toLowerCase().includes(needle)));
  }, [all, columns, query]);
  const shown = filtered.slice(0, 500);
  // Platforms report different fields; hide columns no row carries.
  const visible = columns.filter((column) => all.some((row) => row[column.key] !== undefined && row[column.key] !== ""));
  const total = num(meta.total) ?? all.length;
  return <div>
    <div className="rmm-list-toolbar">
      <label className="rmm-filter"><span className="sr-only">Filter {noun}</span>
        <input className="input" type="search" placeholder={`Filter ${noun}`} value={query} onChange={(event) => setQuery(event.target.value)} /></label>
      <span className="muted">{query ? `${filtered.length} of ${all.length}` : `${all.length}`} {all.length === 1 && !query ? singular : noun}{total > all.length ? `; the agent found ${total} and sent the first ${all.length}` : ""}{meta.source ? `, from ${String(meta.source)}` : ""}</span>
    </div>
    {shown.length ? <div className="table-wrap"><table className="data-table"><thead><tr>
      {visible.map((column) => <th key={column.key} className={column.numeric ? "numeric" : undefined}>{column.label}</th>)}
      {actions.length > 0 && <th><span className="sr-only">Actions</span></th>}</tr></thead><tbody>
      {shown.map((row, index) => <tr key={`${text(row.pid ?? row.name)}-${index}`}>
        {visible.map((column) => <td key={column.key} className={column.numeric ? "numeric mono" : undefined}>{column.render ? column.render(row) : text(row[column.key])}</td>)}
        {actions.length > 0 && <td className="rmm-row-actions">{actions.map((action) => {
          const parameters = action.parameters(row);
          return parameters && <button key={action.type} className="button button-small" type="button" disabled={busy}
            onClick={() => onAction?.(action.type, parameters)}>{action.label}</button>;
        })}</td>}
      </tr>)}
    </tbody></table>{filtered.length > shown.length && <p className="rmm-note muted">Showing the first 500 of {filtered.length}; filter to narrow the list.</p>}</div>
      : <p className="rmm-note muted">{all.length ? `No ${noun} match “${query}”.` : `The agent reported no ${noun}.`}</p>}
  </div>;
}

const serviceStatus: Record<string, string> = { running: "online", active: "online", stopped: "stopped", inactive: "stopped", exited: "stopped", dead: "stopped", failed: "problem" };

export function ServiceState({ row }: { row: Row }) {
  const state = String(row.subState ?? row.state ?? "unknown");
  const shown = row.subState && row.state && row.subState !== row.state ? `${String(row.state)} (${String(row.subState)})` : state;
  return <span className="device-status"><span className="status-dot" data-status={serviceStatus[state] ?? serviceStatus[String(row.state)] ?? "unknown"} aria-hidden="true" />{shown}</span>;
}

export const processColumns: Column[] = [
  { key: "pid", label: "PID", numeric: true },
  { key: "name", label: "Name" },
  { key: "user", label: "User" },
  { key: "memoryBytes", label: "Memory", numeric: true, render: (row) => num(row.memoryBytes) === undefined ? "—" : formatBytes(num(row.memoryBytes)!) },
  { key: "threads", label: "Threads", numeric: true },
  { key: "state", label: "State" },
];

export const serviceColumns: Column[] = [
  { key: "name", label: "Name" },
  { key: "displayName", label: "Display name" },
  { key: "state", label: "State", render: (row) => <ServiceState row={row} /> },
  { key: "pid", label: "PID", numeric: true },
];

export const softwareColumns: Column[] = [
  { key: "name", label: "Name" },
  { key: "version", label: "Version" },
  { key: "architecture", label: "Architecture" },
  { key: "scope", label: "Scope" },
];

// operatingSystem prefers the distribution name; "linux" alone says little.
export function operatingSystem(info: Row) {
  return text(info.osVersion || info.os);
}

function Property({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><dt>{label}</dt><dd className={mono && value !== "—" ? "mono" : undefined}>{value}</dd></div>;
}
