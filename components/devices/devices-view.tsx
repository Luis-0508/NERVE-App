"use client";

import Link from "next/link";
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, Columns3, MonitorCog, Play, Radar, RefreshCw, Save, Search, SlidersHorizontal } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceFacets, DeviceList, DeviceSummary, DiscoveryState } from "@/lib/types";
import { DeviceStatus } from "./device-status";

type SortKey = "name" | "status" | "classification" | "hostname" | "ip" | "vendor" | "first_seen" | "last_seen";
type Direction = "asc" | "desc";
type Density = "compact" | "normal";

const pageSize = 50;

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function displayValue(value?: string) { return value || "—"; }

export function DevicesView() {
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [classification, setClassification] = useState("");
  const [sort, setSort] = useState<SortKey>("name");
  const [direction, setDirection] = useState<Direction>("asc");
  const [offset, setOffset] = useState(0);
  const [density, setDensity] = useState<Density>("compact");
  const [result, setResult] = useState<DeviceList | null>(null);
  const [facets, setFacets] = useState<DeviceFacets>({ statuses: [], classifications: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [authRequired, setAuthRequired] = useState(false);
  const requestSequence = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => { setSearch(searchInput.trim()); setOffset(0); }, 250);
    return () => window.clearTimeout(timer);
  }, [searchInput]);

  useEffect(() => {
    api.deviceFacets().then(setFacets).catch(() => undefined);
  }, []);

  const load = useCallback(async () => {
    const sequence = ++requestSequence.current;
    setLoading(true); setError(null); setAuthRequired(false);
    try {
      const devices = await api.devices({ search, status, classification, sort, direction, limit: pageSize, offset });
      if (sequence === requestSequence.current) setResult(devices);
    } catch (requestError) {
      if (sequence !== requestSequence.current) return;
      if (requestError instanceof APIError && requestError.status === 401) setAuthRequired(true);
      else setError(requestError instanceof Error ? requestError.message : "Devices could not be loaded.");
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [classification, direction, offset, search, sort, status]);

  useEffect(() => { void load(); }, [load]);

  function changeSort(next: SortKey) {
    if (sort === next) setDirection((current) => current === "asc" ? "desc" : "asc");
    else { setSort(next); setDirection("asc"); }
    setOffset(0);
  }

  function sortButton(label: string, key: SortKey) {
    const active = sort === key;
    const Icon = direction === "asc" ? ArrowUp : ArrowDown;
    return <button className="table-sort" type="button" onClick={() => changeSort(key)} aria-label={`Sort by ${label}${active ? `, currently ${direction}ending` : ""}`}><span>{label}</span>{active && <Icon aria-hidden="true" />}</button>;
  }

  const shownStart = result && result.total > 0 ? result.offset + 1 : 0;
  const shownEnd = result ? Math.min(result.offset + result.items.length, result.total) : 0;
  const filtersActive = Boolean(search || status || classification);

  return <div className="page devices-page">
    <div className="page-heading">
      <div><h1>Devices</h1><p>Physical and logical network identities known to NERVE.</p></div>
      <button className="button" type="button" onClick={() => void load()} disabled={loading}><RefreshCw size={14} aria-hidden="true" />Refresh</button>
    </div>

    <DiscoveryPanel onCompleted={load} />

    <section className="device-table-panel" aria-label="Device inventory">
      <div className="device-toolbar">
        <label className="search-control"><Search aria-hidden="true" /><span className="sr-only">Search devices</span><input value={searchInput} onChange={(event) => setSearchInput(event.target.value)} placeholder="Search name, hostname, IP, MAC, vendor" /></label>
        <div className="filter-control"><SlidersHorizontal aria-hidden="true" /><label htmlFor="device-status-filter">Status</label><select id="device-status-filter" value={status} onChange={(event) => { setStatus(event.target.value); setOffset(0); }}><option value="">All</option>{facets.statuses.map((item) => <option key={item} value={item}>{item}</option>)}</select></div>
        <div className="filter-control"><label htmlFor="device-type-filter">Type</label><select id="device-type-filter" value={classification} onChange={(event) => { setClassification(event.target.value); setOffset(0); }}><option value="">All</option>{facets.classifications.map((item) => <option key={item} value={item}>{item}</option>)}</select></div>
        <button className="icon-button density-button" type="button" onClick={() => setDensity((current) => current === "compact" ? "normal" : "compact")} aria-label={`Table density: ${density}. Change density.`} title={`Density: ${density}`}><Columns3 aria-hidden="true" /></button>
      </div>

      {authRequired ? <div className="empty-state"><MonitorCog aria-hidden="true" /><h2>Authentication required</h2><p>Sign in to read the device inventory.</p><p><Link className="button button-primary" href="/login">Sign in</Link></p></div>
      : error ? <div className="error-state"><h2>Device inventory unavailable</h2><p>{error}</p><button className="button" type="button" onClick={() => void load()}>Retry</button></div>
      : loading && !result ? <DeviceTableLoading />
      : result && result.items.length > 0 ? <DeviceTable items={result.items} density={density} sortButton={sortButton} />
      : <div className="empty-state"><MonitorCog aria-hidden="true" /><h2>{filtersActive ? "No matching devices" : "No devices discovered"}</h2><p>{filtersActive ? "Change or clear the current filters." : "The inventory will populate when a discovery source or integration records device observations."}</p>{filtersActive && <button className="button" type="button" onClick={() => { setSearchInput(""); setSearch(""); setStatus(""); setClassification(""); setOffset(0); }}>Clear filters</button>}</div>}

      <footer className="table-footer">
        <span>{loading && result ? "Updating…" : `${shownStart}–${shownEnd} of ${result?.total ?? 0}`}</span>
        <div><button className="icon-button" type="button" disabled={!result || offset === 0 || loading} onClick={() => setOffset(Math.max(0, offset - pageSize))} aria-label="Previous page"><ChevronLeft aria-hidden="true" /></button><button className="icon-button" type="button" disabled={!result || offset + pageSize >= result.total || loading} onClick={() => setOffset(offset + pageSize)} aria-label="Next page"><ChevronRight aria-hidden="true" /></button></div>
      </footer>
    </section>
  </div>;
}

function DiscoveryPanel({ onCompleted }: { onCompleted: () => void | Promise<void> }) {
  const [state, setState] = useState<DiscoveryState | null>(null);
  const [cidr, setCIDR] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const previousStatus = useRef<string | undefined>(undefined);

  const refresh = useCallback(async () => {
    try {
      const next = await api.discovery();
      setState(next);
      setError(null);
      setCIDR((current) => current || next.target?.cidr || "");
      const completed = previousStatus.current === "queued" || previousStatus.current === "running";
      if (completed && next.scan && !["queued", "running"].includes(next.scan.status)) void onCompleted();
      previousStatus.current = next.scan?.status;
    } catch (requestError) {
      if (!(requestError instanceof APIError && requestError.status === 401)) {
        setError(requestError instanceof Error ? requestError.message : "Discovery status could not be loaded.");
      }
    }
  }, [onCompleted]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2500);
    return () => window.clearInterval(timer);
  }, [refresh]);

  async function saveTarget() {
    setBusy(true); setError(null);
    try {
      const target = await api.saveDiscoveryTarget(cidr.trim());
      setState((current) => ({ ...current, target }));
      setCIDR(target.cidr);
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The subnet could not be saved.");
    } finally { setBusy(false); }
  }

  async function startScan() {
    if (!state?.target) return;
    setBusy(true); setError(null);
    try {
      const scan = await api.startDiscoveryScan(state.target.id);
      previousStatus.current = scan.status;
      setState((current) => ({ ...current, scan }));
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The scan could not be started.");
    } finally { setBusy(false); }
  }

  const scanActive = state?.scan?.status === "queued" || state?.scan?.status === "running";
  const targetChanged = cidr.trim() !== (state?.target?.cidr ?? "");
  const scanBlockedReason = busy
    ? "Discovery is processing the current request."
    : scanActive
      ? "A discovery scan is already running."
      : !state?.target
        ? "Enter a subnet and save it before starting a scan."
        : targetChanged
          ? "Save the changed subnet before starting a scan."
          : !state.agent?.online
            ? "The local discovery agent must be online."
            : undefined;
  return <section className="discovery-panel" aria-label="Local network discovery">
    <div className="discovery-config">
      <div className="discovery-title"><Radar aria-hidden="true" /><div><h2>Local discovery</h2><p>Scans run through the NERVE agent on this computer.</p></div></div>
      <label className="discovery-cidr"><span>Subnet (CIDR)</span><input className="input mono" value={cidr} onChange={(event) => setCIDR(event.target.value)} placeholder="Example: 192.168.178.0/24" spellCheck={false} /></label>
      <button className="button" type="button" aria-busy={busy} disabled={busy || !cidr.trim() || !targetChanged} title={!cidr.trim() ? "Enter a subnet first." : !targetChanged ? "This subnet is already saved." : undefined} onClick={() => void saveTarget()}><Save aria-hidden="true" />{busy ? "Saving…" : "Save subnet"}</button>
      <button className="button button-primary" type="button" aria-busy={busy || scanActive} aria-describedby={scanBlockedReason ? "discovery-scan-hint" : undefined} disabled={Boolean(scanBlockedReason)} title={scanBlockedReason} onClick={() => void startScan()}><Play aria-hidden="true" />{scanActive ? "Scanning…" : "Scan now"}</button>
    </div>
    <div className="discovery-status">
      <span><i className="status-dot" data-status={state?.agent?.online ? "online" : "offline"} />Agent {state?.agent?.online ? `online · ${state.agent.hostname}` : "offline"}</span>
      {state?.scan ? <span>Last scan: <strong>{state.scan.status}</strong> · {state.scan.addressesReachable}/{state.scan.addressesTotal || "…"} reachable · {state.scan.errorCount} errors</span> : <span>No scan has run yet.</span>}
      {scanBlockedReason && !busy && !scanActive && <span className="discovery-hint" id="discovery-scan-hint">{scanBlockedReason}</span>}
    </div>
    {(error || state?.agent?.lastError || state?.scan?.errorMessage) && <div className="discovery-error" role="alert">{error || state?.agent?.lastError || state?.scan?.errorMessage}</div>}
    {state?.scan?.errors && state.scan.errors.length > 0 && <details className="discovery-errors"><summary>Show recent scan errors ({state.scan.errorCount})</summary><ul>{state.scan.errors.map((item, index) => <li key={`${item.address}-${item.stage}-${index}`}><span className="mono">{item.address || "scan"}</span> · {item.stage}: {item.message}</li>)}</ul></details>}
  </section>;
}

function DeviceTable({ items, density, sortButton }: { items: DeviceSummary[]; density: Density; sortButton: (label: string, key: SortKey) => React.ReactNode }) {
  return <div className="table-wrap"><table className="data-table device-table" data-density={density}>
    <thead><tr><th>{sortButton("Device", "name")}</th><th>{sortButton("Status", "status")}</th><th>{sortButton("IP address", "ip")}</th><th>{sortButton("Hostname", "hostname")}</th><th>MAC address</th><th>{sortButton("Vendor", "vendor")}</th><th>{sortButton("Type", "classification")}</th><th>{sortButton("Last seen", "last_seen")}</th></tr></thead>
    <tbody>{items.map((device) => <tr key={device.id}><td><Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(device.id)}`}>{device.name}</Link></td><td><DeviceStatus status={device.status} /></td><td className="mono">{displayValue(device.primaryIpAddress)}</td><td>{displayValue(device.hostname)}</td><td className="mono">{displayValue(device.primaryMac)}</td><td>{displayValue(device.vendor)}</td><td>{device.classification}</td><td title={formatDate(device.lastSeenAt)}>{formatDate(device.lastSeenAt)}</td></tr>)}</tbody>
  </table></div>;
}

function DeviceTableLoading() {
  return <div className="table-wrap" aria-label="Loading devices"><table className="data-table device-table"><thead><tr><th>Device</th><th>Status</th><th>IP address</th><th>Hostname</th><th>MAC address</th><th>Vendor</th><th>Type</th><th>Last seen</th></tr></thead><tbody>{Array.from({ length: 6 }, (_, index) => <tr key={index}>{Array.from({ length: 8 }, (_, cell) => <td key={cell}><span className="skeleton-line" /></td>)}</tr>)}</tbody></table></div>;
}
