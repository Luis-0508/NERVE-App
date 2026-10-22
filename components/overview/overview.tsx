"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceSummary, DiscoveryState, DuplicateCandidate, NerveEvent, ServiceStatus } from "@/lib/types";
import { useDeviceInventory } from "@/components/map/use-device-inventory";

type Shape = "online" | "offline" | "warning" | "problem" | "limited" | "operator" | "info" | "unknown";
type Attention = { key: string; shape: Shape; title: string; detail: string; source: string; address?: string; at?: string; href: string };
type CellKind = "online" | "offline" | "warning" | "unknown" | "never";
type Cell = { address: string; kind: CellKind; device?: DeviceSummary };

const ATTENTION_LIMIT = 8;
const NEW_DEVICE_WINDOW_MS = 24 * 60 * 60 * 1000;
const NEW_DEVICE_GROUP_AT = 3;

function relative(iso?: string, now = Date.now()) {
  if (!iso) return "";
  const seconds = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} min`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} h`;
  return `${Math.floor(seconds / 86400)} d`;
}

function clock(iso: string) {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function formatUptime(seconds?: number) {
  if (seconds === undefined) return "";
  const days = Math.floor(seconds / 86400), hours = Math.floor((seconds % 86400) / 3600), minutes = Math.floor((seconds % 3600) / 60);
  if (days) return `${days} d ${hours} h`;
  if (hours) return `${hours} h ${minutes} min`;
  return `${minutes} min`;
}

const deviceHref = (id: string) => `/devices/detail?id=${encodeURIComponent(id)}`;

function ipv4ToInt(value: string) {
  const parts = value.split(".");
  if (parts.length !== 4) return null;
  let result = 0;
  for (const part of parts) {
    if (!/^\d{1,3}$/.test(part) || Number(part) > 255) return null;
    result = result * 256 + Number(part);
  }
  return result;
}

// Builds the address map for IPv4 targets of at most 256 addresses. Devices are
// placed by their primary address, which is the only address the list returns.
function addressMap(cidr: string | undefined, devices: DeviceSummary[]) {
  if (!cidr) return null;
  const [network, prefixText] = cidr.split("/");
  const prefix = Number(prefixText), base = ipv4ToInt(network ?? "");
  if (base === null || !Number.isInteger(prefix) || prefix < 24 || prefix > 32) return null;
  const size = 2 ** (32 - prefix);
  const start = base - (base % size);
  const byOffset = new Map<number, DeviceSummary>();
  let outside = 0;
  for (const device of devices) {
    const value = device.primaryIpAddress ? ipv4ToInt(device.primaryIpAddress) : null;
    if (value === null) continue;
    if (value >= start && value < start + size) byOffset.set(value - start, device);
    else outside += 1;
  }
  const prefixText3 = network.split(".").slice(0, 3).join(".");
  const cells: Cell[] = [];
  for (let offset = 0; offset < size; offset++) {
    const device = byOffset.get(offset);
    const host = (start % 256) + offset;
    const address = `${prefixText3}.${host}`;
    let kind: CellKind = "never";
    if (device) kind = device.status === "online" ? "online" : device.status === "offline" || device.status === "stopped" ? "offline" : device.status === "warning" ? "warning" : "unknown";
    cells.push({ address, kind, device });
  }
  const rows: Array<{ label: string; cells: Cell[] }> = [];
  for (let index = 0; index < cells.length; index += 16) rows.push({ label: `.${(start % 256) + index}`, cells: cells.slice(index, index + 16) });
  const count = (kind: CellKind) => cells.filter((cell) => cell.kind === kind).length;
  return { rows, outside, counts: { online: count("online"), offline: count("offline"), warning: count("warning"), unknown: count("unknown"), never: count("never") } };
}

function attentionItems(discovery: DiscoveryState | null, duplicates: DuplicateCandidate[], devices: DeviceSummary[], now: number): Attention[] {
  const items: Attention[] = [];
  if (discovery) {
    const { agent, target, scan } = discovery;
    if (!target) items.push({ key: "no-target", shape: "warning", title: "No subnet configured", detail: "Discovery has nothing to scan until a subnet is saved.", source: "discovery settings", href: "/devices" });
    if (!agent) items.push({ key: "no-agent", shape: "problem", title: "No discovery agent reporting", detail: "Scans cannot run until the host agent sends a heartbeat.", source: "discovery heartbeat", href: "/devices" });
    else if (!agent.online) items.push({ key: "agent-offline", shape: "problem", title: `Discovery agent ${agent.hostname} is offline`, detail: agent.lastError ?? "No recent heartbeat.", source: "discovery heartbeat", at: agent.lastHeartbeatAt, href: "/devices" });
    if (scan?.status === "failed") items.push({ key: "scan-failed", shape: "problem", title: "Last scan failed", detail: scan.errorMessage ?? `${scan.errorCount} errors.`, source: `scan of ${scan.targetCidr}`, at: scan.completedAt ?? scan.requestedAt, href: "/devices" });
    else if (scan?.status === "partial") items.push({ key: "scan-partial", shape: "warning", title: "Last scan finished with errors", detail: `${scan.errorCount} errors while scanning ${scan.addressesTotal} addresses.`, source: `scan of ${scan.targetCidr}`, at: scan.completedAt, href: "/devices" });
  }
  for (const device of devices) {
    if (device.trustState === "trusted" && device.status === "offline") items.push({ key: `offline-${device.id}`, shape: "problem", title: `${device.name} is offline`, detail: "Trusted device stopped answering discovery checks.", source: "discovery", address: device.primaryIpAddress, at: device.lastSeenAt, href: deviceHref(device.id) });
  }
  for (const device of devices) {
    if (device.status === "warning") items.push({ key: `warning-${device.id}`, shape: "warning", title: `${device.name} reports a warning`, detail: "Open the device to see which check is degraded.", source: "device status", address: device.primaryIpAddress, at: device.lastSeenAt, href: deviceHref(device.id) });
  }
  for (const candidate of duplicates) {
    items.push({ key: `dup-${candidate.deviceA.id}-${candidate.deviceB.id}`, shape: "warning", title: `Possible duplicate: ${candidate.deviceA.name} and ${candidate.deviceB.name}`, detail: candidate.reasons[0] ?? "Shared identity signals.", source: `identity evidence, ${candidate.confidence} confidence`, href: deviceHref(candidate.deviceA.id) });
  }
  const fresh = devices.filter((device) => device.trustState === "unknown" && now - new Date(device.firstSeenAt).getTime() < NEW_DEVICE_WINDOW_MS);
  if (fresh.length > NEW_DEVICE_GROUP_AT) {
    // A first scan finds every device at once; one summary keeps the real problems visible.
    const names = fresh.slice(0, 3).map((device) => device.name).join(", ");
    const latest = fresh.reduce((last, device) => (device.firstSeenAt > last ? device.firstSeenAt : last), fresh[0].firstSeenAt);
    items.push({ key: "new-devices", shape: "warning", title: `${fresh.length} new devices to review`, detail: `First seen in the last 24 hours and not reviewed yet: ${names} and ${fresh.length - 3} more.`, source: "discovery", at: latest, href: "/devices?trust=unknown" });
  } else {
    for (const device of fresh) items.push({ key: `new-${device.id}`, shape: "warning", title: `New device: ${device.name}`, detail: "First seen in the last 24 hours and not reviewed yet.", source: "discovery", address: device.primaryIpAddress, at: device.firstSeenAt, href: deviceHref(device.id) });
  }
  return items;
}

const eventShape = (severity: NerveEvent["severity"]): Shape => (severity === "critical" ? "problem" : severity === "warning" ? "warning" : "info");

export function Overview() {
  const [status, setStatus] = useState<ServiceStatus | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [events, setEvents] = useState<NerveEvent[] | null>(null);
  const [discovery, setDiscovery] = useState<DiscoveryState | null>(null);
  const [duplicates, setDuplicates] = useState<DuplicateCandidate[]>([]);
  const [authRequired, setAuthRequired] = useState(false);
  const [dataError, setDataError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const inventory = useDeviceInventory(!authRequired && status !== null);

  const load = useCallback(async () => {
    setNow(Date.now());
    try { setStatus(await api.status()); setStatusError(null); }
    catch (error) { setStatusError(error instanceof Error ? error.message : "The core service could not be reached."); return; }
    // Each source is optional: a role without one permission still sees the rest.
    const [eventList, discoveryState, duplicateList] = await Promise.allSettled([api.events(12), api.discovery(), api.deviceDuplicates()]);
    const failures = [eventList, discoveryState, duplicateList].filter((result) => result.status === "rejected").map((result) => (result as PromiseRejectedResult).reason);
    if (failures.some((reason) => reason instanceof APIError && reason.status === 401)) { setAuthRequired(true); return; }
    setAuthRequired(false);
    if (eventList.status === "fulfilled") setEvents(eventList.value.items);
    if (discoveryState.status === "fulfilled") setDiscovery(discoveryState.value);
    if (duplicateList.status === "fulfilled") setDuplicates(duplicateList.value.items);
    setDataError(failures.length ? `Some overview data could not be loaded: ${failures.map((reason) => (reason instanceof Error ? reason.message : String(reason))).join(" ")}` : null);
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 30_000);
    return () => window.clearInterval(timer);
  }, [load]);

  const attention = useMemo(() => attentionItems(discovery, duplicates, inventory.devices, now), [discovery, duplicates, inventory.devices, now]);
  const map = useMemo(() => addressMap(discovery?.target?.cidr, inventory.devices), [discovery?.target?.cidr, inventory.devices]);
  const scan = discovery?.scan;

  if (statusError) {
    return <div className="page"><div className="error-state" role="alert"><h3>Core service unavailable</h3><p>{statusError} Check that the API and PostgreSQL services are running, then retry.</p><button className="button" type="button" onClick={() => void load()}>Retry connection</button></div></div>;
  }

  return (
    <div className="overview">
      <div className="overview-context">
        <div className="overview-scope">
          {discovery?.target ? <span className="mono overview-cidr">{discovery.target.cidr}</span> : <span className="overview-cidr">Local network</span>}
          {scan ? <span>Last scan {scan.completedAt ? clock(scan.completedAt) : "running"}, {scan.addressesReachable} of {scan.addressesTotal} answered, {scan.errorCount} {scan.errorCount === 1 ? "error" : "errors"}</span> : discovery ? <span className="muted">No scan has run yet</span> : null}
        </div>
        {!authRequired && <Link className="button" href="/devices">Manage discovery</Link>}
      </div>

      <div className="overview-columns">
        <section className="overview-col" aria-labelledby="attention-title">
          <div className="overview-col-head"><h1 id="attention-title">Needs attention</h1>{!authRequired && discovery && <span className="overview-count" data-empty={attention.length === 0}>{attention.length}</span>}</div>
          {dataError && !authRequired && <div className="error-state" role="alert"><h3>Partial data</h3><p>{dataError}</p><button className="button" type="button" onClick={() => void load()}>Retry</button></div>}
          {authRequired ? (
            <div className="overview-note"><p>Sign in to see devices, discovery and events. Core health stays visible without a session.</p><Link className="button button-primary" href="/login">Sign in</Link></div>
          ) : !discovery && !dataError ? (
            <p className="overview-note muted">Loading discovery state…</p>
          ) : attention.length === 0 ? (
            discovery && !inventory.error && inventory.loaded ? <p className="overview-note">Nothing needs you right now. Offline trusted devices, new devices, duplicates and discovery problems will appear here.</p> : null
          ) : (
            <>
              <ul className="attention-list">
                {attention.slice(0, ATTENTION_LIMIT).map((item) => (
                  <li key={item.key} className="attention-item">
                    <span className="status-dot" data-status={item.shape} aria-hidden="true" />
                    <div className="attention-body">
                      <Link className="attention-title" href={item.href}>{item.title}</Link>
                      <div>{item.detail}</div>
                      <div className="attention-source">Source: {item.source}</div>
                    </div>
                    <div className="attention-meta">{item.address && <span className="mono">{item.address}</span>}{item.at && <time dateTime={item.at} title={new Date(item.at).toLocaleString()}>{relative(item.at, now)}</time>}</div>
                  </li>
                ))}
              </ul>
              {attention.length > ATTENTION_LIMIT && <p className="overview-more muted">{attention.length - ATTENTION_LIMIT} more not shown. <Link href="/devices">Open Devices</Link></p>}
            </>
          )}
          {inventory.error && !authRequired && <p className="overview-more muted">{inventory.error} Device-based checks are missing.</p>}
        </section>

        <section className="overview-col" aria-labelledby="activity-title">
          <div className="overview-col-head"><h2 id="activity-title">Recent activity</h2></div>
          {authRequired ? <p className="overview-note muted">Events need a session.</p> : events === null ? <p className="overview-note muted">Loading events…</p> : events.length === 0 ? (
            <p className="overview-note">No events recorded yet. Events appear after discovery or an integration produces its first observation.</p>
          ) : (
            <ol className="activity-list">
              {events.map((event) => (
                <li key={event.id} className="activity-item">
                  <time className="mono" dateTime={event.occurredAt} title={new Date(event.occurredAt).toLocaleString()}>{clock(event.occurredAt)}</time>
                  <span className="status-dot" data-status={eventShape(event.severity)} aria-label={event.severity} />
                  <div><div>{event.message}</div><div className="attention-source">{event.category}</div></div>
                </li>
              ))}
            </ol>
          )}
        </section>

        <aside className="overview-col overview-rail" aria-labelledby="coverage-title">
          <div className="overview-col-head"><h2 id="coverage-title">Coverage</h2></div>
          {!authRequired && map && (
            <figure className="address-map">
              <div className="address-grid" role="img" aria-label={`Address map of ${discovery?.target?.cidr}: ${map.counts.online} online, ${map.counts.offline} offline, ${map.counts.warning} warning, ${map.counts.unknown} unknown, ${map.counts.never} never seen`}>
                {map.rows.map((row) => (
                  <div className="address-row" key={row.label}>
                    <span className="address-tick mono" aria-hidden="true">{row.label}</span>
                    {row.cells.map((cell) => cell.device
                      ? <Link key={cell.address} className="address-cell" data-kind={cell.kind} href={deviceHref(cell.device.id)} title={`${cell.address} ${cell.device.name}, ${cell.device.status}`} aria-label={`${cell.address} ${cell.device.name}, ${cell.device.status}`} />
                      : <span key={cell.address} className="address-cell" data-kind="never" />)}
                  </div>
                ))}
              </div>
              <figcaption>
                <dl className="address-legend">
                  <div><dt><span className="address-cell" data-kind="online" />Online</dt><dd className="mono">{map.counts.online}</dd></div>
                  <div><dt><span className="address-cell" data-kind="offline" />Offline</dt><dd className="mono">{map.counts.offline}</dd></div>
                  {map.counts.warning > 0 && <div><dt><span className="address-cell" data-kind="warning" />Warning</dt><dd className="mono">{map.counts.warning}</dd></div>}
                  {map.counts.unknown > 0 && <div><dt><span className="address-cell" data-kind="unknown" />Unknown</dt><dd className="mono">{map.counts.unknown}</dd></div>}
                  <div><dt><span className="address-cell" data-kind="never" />Never seen</dt><dd className="mono">{map.counts.never}</dd></div>
                </dl>
                <p className="muted">Placed by primary address.{map.outside > 0 ? ` ${map.outside} ${map.outside === 1 ? "device is" : "devices are"} outside this subnet.` : ""}{inventory.truncated ? " Inventory is truncated." : ""}</p>
              </figcaption>
            </figure>
          )}
          {!authRequired && discovery?.target && !map && <p className="overview-note muted">The address map is shown for IPv4 subnets of up to 256 addresses.</p>}
          <dl className="source-list">
            {discovery && (
              <div className="source-item">
                <dt><span className="status-dot" data-status={discovery.agent ? (discovery.agent.online ? "online" : "problem") : "unknown"} aria-hidden="true" />Discovery agent</dt>
                <dd>{discovery.agent ? `${discovery.agent.hostname}, heartbeat ${relative(discovery.agent.lastHeartbeatAt, now)} ago` : "Not reporting"}</dd>
              </div>
            )}
            <div className="source-item">
              <dt><span className="status-dot" data-status={status?.database.status === "ok" ? "online" : status ? "problem" : "unknown"} aria-hidden="true" />Database</dt>
              <dd>{status ? (status.database.status === "ok" ? (status.database.latencyMs !== undefined ? `${status.database.latencyMs} ms` : "Reachable") : "Unavailable") : "Checking"}</dd>
            </div>
            <div className="source-item">
              <dt><span className="status-dot" data-status={status ? (status.status === "ok" ? "online" : "warning") : "unknown"} aria-hidden="true" />Core</dt>
              <dd>{status ? `Up ${formatUptime(status.uptimeSeconds)}, version ${status.version}` : "Checking"}</dd>
            </div>
          </dl>
        </aside>
      </div>
    </div>
  );
}
