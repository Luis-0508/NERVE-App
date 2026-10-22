"use client";

import Link from "next/link";
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, Columns3, MonitorCog, Play, Radar, RefreshCw, Save, Search, SlidersHorizontal, Square } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceFacets, DeviceList, DeviceSummary, DiscoveryState, DuplicateCandidate } from "@/lib/types";
import { TrustBadge } from "./device-identity-panel";
import { DeviceStatus } from "./device-status";

// The shell's device search links here as /devices?search=...; apply it to the search box.
function SearchFromUrl({ onSearch }: { onSearch: (value: string) => void }) {
  const value = useSearchParams().get("search");
  useEffect(() => { if (value !== null) onSearch(value); }, [value, onSearch]);
  return null;
}

type SortKey = "name" | "status" | "classification" | "trust" | "hostname" | "ip" | "vendor" | "first_seen" | "last_seen";
type Direction = "asc" | "desc";
type Density = "compact" | "normal";

const pageSize = 50;

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function displayValue(value?: string) { return value || "—"; }

function addressCountForCIDR(cidr?: string) {
  if (!cidr) return undefined;
  const [address, prefixText] = cidr.split("/");
  const prefix = Number(prefixText);
  const bits = address.includes(":") ? 128 : 32;
  if (!Number.isInteger(prefix) || prefix < 0 || prefix > bits) return undefined;
  const hostBits = bits - prefix;
  if (hostBits > 12) return undefined;
  let total = 2 ** hostBits;
  if (bits === 32 && prefix <= 30) total = Math.max(0, total - 2);
  return total;
}

function formatElapsed(startedAt?: string) {
  if (!startedAt) return "";
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${seconds % 60}s`;
}

export function DevicesView() {
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [classification, setClassification] = useState("");
  const [trust, setTrust] = useState("");
  const [tag, setTag] = useState("");
  const [sort, setSort] = useState<SortKey>("name");
  const [direction, setDirection] = useState<Direction>("asc");
  const [offset, setOffset] = useState(0);
  const [density, setDensity] = useState<Density>("compact");
  const [result, setResult] = useState<DeviceList | null>(null);
  const [facets, setFacets] = useState<DeviceFacets>({ statuses: [], classifications: [], trustStates: [], tags: [] });
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
      const devices = await api.devices({ search, status, classification, trust, tag, sort, direction, limit: pageSize, offset });
      if (sequence === requestSequence.current) setResult(devices);
    } catch (requestError) {
      if (sequence !== requestSequence.current) return;
      if (requestError instanceof APIError && requestError.status === 401) setAuthRequired(true);
      else setError(requestError instanceof Error ? requestError.message : "Devices could not be loaded.");
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [classification, direction, offset, search, sort, status, tag, trust]);

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
  const filtersActive = Boolean(search || status || classification || trust || tag);

  return <div className="page devices-page">
    <Suspense fallback={null}><SearchFromUrl onSearch={setSearchInput} /></Suspense>
    <div className="page-heading">
      <div><h1>Devices</h1><p>Physical and logical network identities known to NERVE.</p></div>
      <button className="button" type="button" onClick={() => void load()} disabled={loading}><RefreshCw size={14} aria-hidden="true" />Refresh</button>
    </div>

    <DiscoveryPanel onCompleted={load} />
    <DuplicatesNotice refreshKey={result} />

    <section className="device-table-panel" aria-label="Device inventory">
      <div className="device-toolbar">
        <label className="search-control"><Search aria-hidden="true" /><span className="sr-only">Search devices</span><input value={searchInput} onChange={(event) => setSearchInput(event.target.value)} placeholder="Search name, alias, hostname, any IP or MAC, tag, vendor" /></label>
        <div className="filter-control"><SlidersHorizontal aria-hidden="true" /><label htmlFor="device-status-filter">Status</label><select id="device-status-filter" value={status} onChange={(event) => { setStatus(event.target.value); setOffset(0); }}><option value="">All</option>{facets.statuses.map((item) => <option key={item} value={item}>{item}</option>)}</select></div>
        <div className="filter-control"><label htmlFor="device-type-filter">Type</label><select id="device-type-filter" value={classification} onChange={(event) => { setClassification(event.target.value); setOffset(0); }}><option value="">All</option>{facets.classifications.map((item) => <option key={item} value={item}>{item}</option>)}</select></div>
        <div className="filter-control"><label htmlFor="device-trust-filter">Trust</label><select id="device-trust-filter" value={trust} onChange={(event) => { setTrust(event.target.value); setOffset(0); }}><option value="">All</option>{(facets.trustStates ?? []).map((item) => <option key={item} value={item}>{item}</option>)}</select></div>
        {(facets.tags ?? []).length > 0 && <div className="filter-control"><label htmlFor="device-tag-filter">Tag</label><select id="device-tag-filter" value={tag} onChange={(event) => { setTag(event.target.value); setOffset(0); }}><option value="">All</option>{(facets.tags ?? []).map((item) => <option key={item} value={item}>{item}</option>)}</select></div>}
        <button className="icon-button density-button" type="button" onClick={() => setDensity((current) => current === "compact" ? "normal" : "compact")} aria-label={`Table density: ${density}. Change density.`} title={`Density: ${density}`}><Columns3 aria-hidden="true" /></button>
      </div>

      {authRequired ? <div className="empty-state"><MonitorCog aria-hidden="true" /><h2>Authentication required</h2><p>Sign in to read the device inventory.</p><p><Link className="button button-primary" href="/login">Sign in</Link></p></div>
      : error ? <div className="error-state"><h2>Device inventory unavailable</h2><p>{error}</p><button className="button" type="button" onClick={() => void load()}>Retry</button></div>
      : loading && !result ? <DeviceTableLoading />
      : result && result.items.length > 0 ? <DeviceTable items={result.items} density={density} sortButton={sortButton} />
      : <div className="empty-state"><MonitorCog aria-hidden="true" /><h2>{filtersActive ? "No matching devices" : "No devices discovered"}</h2><p>{filtersActive ? "Change or clear the current filters." : "The inventory will populate when a discovery source or integration records device observations."}</p>{filtersActive && <button className="button" type="button" onClick={() => { setSearchInput(""); setSearch(""); setStatus(""); setClassification(""); setTrust(""); setTag(""); setOffset(0); }}>Clear filters</button>}</div>}

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
  const previousReachable = useRef(0);

  const refresh = useCallback(async () => {
    try {
      const next = await api.discovery();
      setState(next);
      setError(null);
      setCIDR((current) => current || next.target?.cidr || "");

      const previousWasActive = previousStatus.current === "queued" || previousStatus.current === "running";
      const nextIsActive = next.scan?.status === "queued" || next.scan?.status === "running";
      const discoveredMore = Boolean(nextIsActive && next.scan && next.scan.addressesReachable > previousReachable.current);
      const completed = Boolean(previousWasActive && next.scan && !nextIsActive);
      if (discoveredMore || completed) void onCompleted();

      previousStatus.current = next.scan?.status;
      previousReachable.current = next.scan?.addressesReachable ?? 0;
    } catch (requestError) {
      if (!(requestError instanceof APIError && requestError.status === 401)) {
        setError(requestError instanceof Error ? requestError.message : "Discovery status could not be loaded.");
      }
    }
  }, [onCompleted]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 1000);
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
    if (!state?.target || state.scan?.status === "queued" || state.scan?.status === "running") return;
    setBusy(true); setError(null);
    try {
      const scan = await api.startDiscoveryScan(state.target.id);
      previousStatus.current = scan.status;
      previousReachable.current = scan.addressesReachable;
      setState((current) => ({ ...current, scan }));
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The scan could not be started.");
    } finally { setBusy(false); }
  }

  async function stopScan() {
    if (!state?.target || !(state.scan?.status === "queued" || state.scan?.status === "running")) return;
    setBusy(true); setError(null);
    try {
      const scan = await api.stopDiscoveryScan(state.target.id);
      previousStatus.current = scan.status;
      previousReachable.current = scan.addressesReachable;
      setState((current) => ({ ...current, scan }));
      await onCompleted();
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The scan could not be stopped.");
    } finally { setBusy(false); }
  }

  const scanActive = state?.scan?.status === "queued" || state?.scan?.status === "running";
  const targetChanged = cidr.trim() !== (state?.target?.cidr ?? "");
  const configurationBlockedReason = busy
    ? "Discovery is processing the current request."
    : !state?.target
      ? "Enter a subnet and save it before starting a scan."
      : targetChanged
        ? "Save the changed subnet before starting a scan."
        : !state.agent?.online
          ? "The local discovery agent must be online."
          : undefined;
  const scanTotal = state?.scan ? state.scan.addressesTotal || addressCountForCIDR(state.scan.targetCidr) : undefined;
  const found = state?.scan?.addressesReachable ?? 0;

  return <section className="discovery-panel" aria-label="Local network discovery">
    <div className="discovery-config">
      <div className="discovery-title"><Radar aria-hidden="true" /><div><h2>Local discovery</h2><p>Scans run through the NERVE agent on this computer.</p></div></div>
      <label className="discovery-cidr"><span>Subnet (CIDR)</span><input className="input mono" value={cidr} onChange={(event) => setCIDR(event.target.value)} placeholder="Example: 192.168.178.0/24" spellCheck={false} /></label>
      <button className="button" type="button" aria-busy={busy} disabled={busy || !cidr.trim() || !targetChanged || scanActive} title={scanActive ? "Stop the current scan before changing the subnet." : !cidr.trim() ? "Enter a subnet first." : !targetChanged ? "This subnet is already saved." : undefined} onClick={() => void saveTarget()}><Save aria-hidden="true" />{busy && !scanActive ? "Saving…" : "Save subnet"}</button>
      {scanActive
        ? <button className="button" type="button" aria-busy={busy} disabled={busy} title="Stop the current discovery scan so another scan can be started." onClick={() => void stopScan()}><Square aria-hidden="true" />{busy ? "Stopping…" : `Stop scan · ${found} found`}</button>
        : <button className="button button-primary" type="button" aria-busy={busy} disabled={Boolean(configurationBlockedReason)} title={configurationBlockedReason} onClick={() => void startScan()}><Play aria-hidden="true" />{busy ? "Starting…" : "Scan now"}</button>}
    </div>
    <div className="discovery-status">
      <span><i className="status-dot" data-status={state?.agent?.online ? "online" : "offline"} />Agent {state?.agent?.online ? `online · ${state.agent.hostname}` : "offline"}</span>
      {scanActive && state?.scan ? <span><strong>Live scan</strong> · {found} found so far{scanTotal !== undefined ? ` · ${scanTotal} addresses` : ""}{state.scan.startedAt ? ` · ${formatElapsed(state.scan.startedAt)}` : ""}</span>
      : state?.scan ? <span>Last scan: <strong>{state.scan.status}</strong> · {state.scan.addressesReachable}/{state.scan.addressesTotal || scanTotal || "…"} reachable · {state.scan.errorCount} errors</span>
      : <span>No scan has run yet.</span>}
      {configurationBlockedReason && !busy && !scanActive && <span className="discovery-hint" id="discovery-scan-hint">{configurationBlockedReason}</span>}
    </div>
    {(error || state?.agent?.lastError || state?.scan?.errorMessage) && <div className="discovery-error" role="alert">{error || state?.agent?.lastError || state?.scan?.errorMessage}</div>}
    {state?.scan?.errors && state.scan.errors.length > 0 && <details className="discovery-errors"><summary>Show recent scan errors ({state.scan.errorCount})</summary><ul>{state.scan.errors.map((item, index) => <li key={`${item.address}-${item.stage}-${index}`}><span className="mono">{item.address || "scan"}</span> · {item.stage}: {item.message}</li>)}</ul></details>}
  </section>;
}

function DeviceTable({ items, density, sortButton }: { items: DeviceSummary[]; density: Density; sortButton: (label: string, key: SortKey) => React.ReactNode }) {
  return <div className="table-wrap"><table className="data-table device-table" data-density={density}>
    <thead><tr><th>{sortButton("Device", "name")}</th><th>{sortButton("Status", "status")}</th><th>{sortButton("Trust", "trust")}</th><th>{sortButton("IP address", "ip")}</th><th>{sortButton("Hostname", "hostname")}</th><th>MAC address</th><th>{sortButton("Vendor", "vendor")}</th><th>{sortButton("Type", "classification")}</th><th>{sortButton("Last seen", "last_seen")}</th></tr></thead>
    <tbody>{items.map((device) => <tr key={device.id}><td><Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(device.id)}`}>{device.name}</Link>{device.displayName && device.discoveredName && device.discoveredName !== device.name && <small className="device-alias">{device.discoveredName}</small>}{device.tags.map((item) => <span key={item} className="tag-chip">{item}</span>)}</td><td><DeviceStatus status={device.status} /></td><td><TrustBadge state={device.trustState} /></td><td className="mono">{displayValue(device.primaryIpAddress)}</td><td>{displayValue(device.hostname)}</td><td className="mono">{displayValue(device.primaryMac)}</td><td>{displayValue(device.vendor)}</td><td>{device.classification}</td><td title={formatDate(device.lastSeenAt)}>{formatDate(device.lastSeenAt)}</td></tr>)}</tbody>
  </table></div>;
}

function DeviceTableLoading() {
  return <div className="table-wrap" aria-label="Loading devices"><table className="data-table device-table"><thead><tr><th>Device</th><th>Status</th><th>Trust</th><th>IP address</th><th>Hostname</th><th>MAC address</th><th>Vendor</th><th>Type</th><th>Last seen</th></tr></thead><tbody>{Array.from({ length: 6 }, (_, index) => <tr key={index}>{Array.from({ length: 9 }, (_, cell) => <td key={cell}><span className="skeleton-line" /></td>)}</tr>)}</tbody></table></div>;
}

/** Shows likely duplicate records; nothing is merged without an operator. */
function DuplicatesNotice({ refreshKey }: { refreshKey: unknown }) {
  const [items, setItems] = useState<DuplicateCandidate[]>([]);
  // Reload whenever the inventory reloads (refresh button, finished scan).
  useEffect(() => { api.deviceDuplicates().then((result) => setItems(result.items)).catch(() => setItems([])); }, [refreshKey]);
  if (items.length === 0) return null;
  return <section className="duplicates-notice" aria-label="Possible duplicate devices">
    <strong>{items.length} possible duplicate record{items.length === 1 ? "" : "s"}</strong>
    <ul>{items.slice(0, 5).map((item) => <li key={`${item.deviceA.id}:${item.deviceB.id}`}>
      <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(item.deviceA.id)}`}>{item.deviceA.name}</Link> and{" "}
      <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(item.deviceB.id)}`}>{item.deviceB.name}</Link>
      <span className="muted"> · {item.confidence}: {item.reasons[0]}</span></li>)}</ul>
    <span className="muted">Review them on a device&apos;s Identity tab. NERVE never merges automatically.</span>
  </section>;
}
