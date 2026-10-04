"use client";

import { LocateFixed, Pause, Play, RefreshCw, Search, X, Settings2 } from "lucide-react";
import dynamic from "next/dynamic";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { TrafficCapture, TrafficFlow, TrafficSnapshot } from "@/lib/types";
import { MapCanvas } from "./map-canvas";
import { Point, validPoint } from "./geometry";

import { legacySiteKey, siteKey, readLocalSite, type LocalSite } from "./local-site";
import { buildTopology } from "./topology";
import { TopologyInspector } from "./topology-inspector";
import { useDeviceInventory } from "./use-device-inventory";

// three.js is only downloaded when the 3D view is shown, and never prerendered.
const TopologyView = dynamic(() => import("./topology-view"), { ssr: false, loading: () => <div className="traffic-empty" role="status"><strong>Loading 3D map…</strong></div> });
const viewKey = "nerve:traffic-map-view";
type View = "topology" | "world";
function speed(bytes: number) { const units = ["bit/s", "Kbit/s", "Mbit/s", "Gbit/s"]; let value = bytes * 8, unit = 0; while (value >= 1000 && unit < 3) { value /= 1000; unit++; } return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`; }
function date(value: string) { const d = new Date(value); return !Number.isFinite(d.getTime()) || d.getUTCFullYear() < 1970 ? "—" : d.toLocaleString(); }
const hints: Record<string, string> = {
 traffic_capture_unavailable: "Run start.bat. Check the NERVE Windows Traffic Sensor service; reports expire after 10 seconds.",
 traffic_capture_permission_denied: "Windows denied the ETW session. Check sensor service permissions and the installation log.",
 traffic_capture_backend_unavailable: "Check the active network adapter and Windows ETW availability. The sensor retries automatically.",
 traffic_source_not_configured: "Optional: run traffic-sensor-setup.bat to connect a capture sensor.",
 traffic_source_authentication_failed: "Check ntopng username, password and interface permissions.",
 traffic_source_unreachable: "Check URL, Docker connectivity, TLS certificate and timeout.",
 traffic_source_invalid_response: "Check the ntopng REST v2 endpoint and interface ID.",
 traffic_source_invalid_configuration: "Use an http(s) base URL and an interface ID, or auto for a source with exactly one interface.",
};
export function TrafficMap() {
 const [snapshot, setSnapshot] = useState<TrafficSnapshot | null>(null), [error, setError] = useState<{ message: string; code: string } | null>(null), [paused, setPaused] = useState(false), [loading, setLoading] = useState(false);
 const [direction, setDirection] = useState("all"), [application, setApplication] = useState("all"), [protocol, setProtocol] = useState("all"), [search, setSearch] = useState(""), [local, setLocal] = useState("all");
 const [origin, setOrigin] = useState<LocalSite | null>(null), [siteOpen, setSiteOpen] = useState(false), [latitude, setLatitude] = useState(""), [longitude, setLongitude] = useState(""), [locationError, setLocationError] = useState("");
 const [selectedID, setSelectedID] = useState<string | null>(null), [diagnostics, setDiagnostics] = useState(false);
 const [siteNotice, setSiteNotice] = useState("");
 const [view, setView] = useState<View>("topology"), [deviceFilter, setDeviceFilter] = useState<"all" | "traffic" | "online">("all"), [selectedNodeID, setSelectedNodeID] = useState<string | null>(null);
 const inventory = useDeviceInventory(view === "topology");
 const [capture, setCapture] = useState<TrafficCapture | undefined>();
 const requestActive = useRef(false), mounted = useRef(false);
 useEffect(() => {
  mounted.current = true;
  try { if (localStorage.getItem(viewKey) === "world") setView("world"); } catch { /* Default view. */ }
  try { const saved = readLocalSite(localStorage); setOrigin(saved.site); setSiteNotice(saved.notice); const value = saved.site ?? saved.candidate; if (value) { setLatitude(String(value.lat)); setLongitude(String(value.lon)); } } catch { setSiteNotice("Browser storage unavailable."); }
  return () => { mounted.current = false; };
 }, []);
 const load = useCallback(async () => {
  if (requestActive.current) return;
  requestActive.current = true; setLoading(true);
  try { const data = await api.liveTraffic(); if (mounted.current) { setSnapshot(data); setCapture(data.capture); setError(null); } }
  catch (e) { if (mounted.current) { setCapture(e instanceof APIError ? e.capture : undefined); setError({ message: e instanceof APIError ? e.message : "NERVE API unreachable", code: e instanceof APIError ? e.code ?? "api_error" : "api_unreachable" }); } }
  finally { requestActive.current = false; if (mounted.current) setLoading(false); }
 }, []);
 useEffect(() => {
  if (paused) return;
  void load();
  const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 2000);
  return () => window.clearInterval(timer);
 }, [load, paused]);
 function saveLocation(point: Point, source: "manual" | "browser" = "manual", accuracyMeters?: number) {
  if (!validPoint(point)) { setLocationError("Latitude must be −90…90; longitude −180…180."); return; }
  const site: LocalSite = { ...point, version: 2, source, savedAt: new Date().toISOString(), ...(source === "browser" ? { accuracyMeters } : {}) };
  setOrigin(site); setSiteNotice(""); setLatitude(String(point.lat)); setLongitude(String(point.lon)); setLocationError("");
  try { localStorage.setItem(siteKey, JSON.stringify(site)); setSiteOpen(false); } catch { setLocationError("Location set for this visit; browser storage is unavailable."); }
 }
 function changeView(next: View) { setView(next); try { localStorage.setItem(viewKey, next); } catch { /* Per-visit only. */ } }
 function useLocation() {
  if (!navigator.geolocation) { setLocationError("Browser geolocation is unavailable. Enter coordinates manually."); return; }
  navigator.geolocation.getCurrentPosition(p => saveLocation({ lat: p.coords.latitude, lon: p.coords.longitude }, "browser", p.coords.accuracy), () => setLocationError("Browser location unavailable or denied. Enter coordinates manually."), { maximumAge: 0, timeout: 10_000 });
 }
 const applications = useMemo(() => [...new Set(snapshot?.flows.map(f => f.application) ?? [])].sort(), [snapshot]);
 const protocols = useMemo(() => [...new Set(snapshot?.flows.map(f => f.protocol) ?? [])].sort(), [snapshot]);
 const localIPs = useMemo(() => [...new Set(snapshot?.flows.map(f => f.localIp) ?? [])].sort(), [snapshot]);
 const matching = useMemo(() => (snapshot?.flows ?? []).filter(f => (direction === "all" || f.direction === direction) && (application === "all" || f.application === application) && (protocol === "all" || f.protocol === protocol) && (local === "all" || f.localIp === local) && `${f.localIp} ${f.remoteIp} ${f.remoteCountryName ?? ""} ${f.application}`.toLowerCase().includes(search.toLowerCase())), [snapshot, direction, application, protocol, local, search]);
 const flows = useMemo(() => error ? [] : matching.filter(f => f.remoteLatitude !== undefined && f.remoteLongitude !== undefined && validPoint({ lat: f.remoteLatitude, lon: f.remoteLongitude })), [matching, error]);
 const selected = snapshot?.flows.find(f => f.id === selectedID);
 const topology = useMemo(() => buildTopology({ devices: inventory.devices, flows: error ? [] : matching, deviceFilter, site: origin ? { lat: origin.lat, lon: origin.lon } : null }), [inventory.devices, matching, error, deviceFilter, origin]);
 const flowIndex = useMemo(() => new Map((snapshot?.flows ?? []).map(f => [f.id, f])), [snapshot]);
 const hostCount = topology.nodes.filter(n => n.kind !== "remote").length, endpointCount = topology.nodes.length - hostCount;
 const topologyMessage = error?.message ?? (!snapshot && !inventory.loaded ? "Connecting to traffic source…" : !topology.nodes.length ? (inventory.devices.length ? "No devices match the device filter" : "No devices or traffic to show yet") : snapshot && !error && !snapshot.summary.activeFlows ? "No active external flows" : null);
 const status = error?.message ?? (!snapshot ? "Connecting to traffic source…" : snapshot.summary.activeFlows === 0 ? "No active external flows" : paused ? "Paused" : "Connected");
 const countries = new Set(flows.map(f => f.remoteCountryCode)).size;
 const message = error?.message ?? (!snapshot ? "Connecting to traffic source…" : !snapshot.summary.activeFlows ? "No active external flows" : !origin ? "Local site not set" : !matching.length ? "No traffic matches the active filters" : !flows.length ? "Active flows have no geographic location" : null);
 return <div className="page traffic-page">
  <header className="traffic-header"><div><div className="eyebrow"><span className={!paused && !error && snapshot ? "live-dot" : "live-dot paused"}/>{error ? "CAPTURE UNAVAILABLE" : paused ? "PAUSED" : snapshot ? "LIVE TRAFFIC" : "CONNECTING"}</div><h1>Traffic Map</h1></div><div className="traffic-actions"><div className="segmented" role="group" aria-label="Map view">{(["topology", "world"] as const).map(item => <button key={item} type="button" onClick={() => changeView(item)} data-active={view === item} aria-pressed={view === item}>{item === "topology" ? "Topology 3D" : "World"}</button>)}</div><button className="button" onClick={() => setPaused(v => !v)}>{paused ? <Play/> : <Pause/>}{paused ? "Resume" : "Pause"}</button><button className="button" disabled={loading} onClick={() => void load()}><RefreshCw/>{loading ? "Checking…" : "Refresh"}</button><button className="button" aria-expanded={diagnostics} onClick={() => setDiagnostics(v => !v)}>Diagnostics</button></div></header>
  <section className={`traffic-stats${error ? " stale" : ""}`} aria-label={error ? "Last successful snapshot" : "Traffic statistics"}><Stat label="Active flows" value={String(snapshot?.summary.activeFlows ?? 0)}/><Stat label="Throughput" value={speed(snapshot?.summary.bytesPerSecond ?? 0)}/><Stat label="↓ Inbound" value={speed(snapshot?.summary.inboundBytesPerSecond ?? 0)}/><Stat label="↑ Outbound" value={speed(snapshot?.summary.outboundBytesPerSecond ?? 0)}/>{view === "topology" ? <Stat label="Hosts / endpoints" value={`${hostCount} / ${endpointCount}`}/> : <Stat label="Mapped / countries" value={`${flows.length} / ${countries}`}/>}</section>
  <section className="traffic-controls"><div className="segmented">{["all", "inbound", "outbound"].map(item => <button key={item} onClick={() => setDirection(item)} data-active={direction === item}>{item === "all" ? "All traffic" : item === "inbound" ? "↓ Inbound" : "↑ Outbound"}</button>)}</div><select aria-label="Application" value={application} onChange={e => setApplication(e.target.value)}><option value="all">All applications</option>{[...new Set([...applications, ...(application !== "all" ? [application] : [])])].map(v => <option key={v}>{v}</option>)}</select><select aria-label="Protocol" value={protocol} onChange={e => setProtocol(e.target.value)}><option value="all">All protocols</option>{[...new Set([...protocols, ...(protocol !== "all" ? [protocol] : [])])].map(v => <option key={v}>{v}</option>)}</select><select aria-label="Local device" value={local} onChange={e => setLocal(e.target.value)}><option value="all">All local devices</option>{[...new Set([...localIPs, ...(local !== "all" ? [local] : [])])].map(v => <option key={v}>{v}</option>)}</select>{view === "topology" && <select aria-label="Devices" value={deviceFilter} onChange={e => setDeviceFilter(e.target.value as typeof deviceFilter)}><option value="all">All devices</option><option value="traffic">Devices with traffic</option><option value="online">Online devices</option></select>}<label className="traffic-search"><Search/><input aria-label="Search flows" value={search} onChange={e => setSearch(e.target.value)} placeholder="Country, application or IP"/></label><button className="button" onClick={() => setSiteOpen(v => !v)} aria-expanded={siteOpen}><Settings2 size={14}/>Local site</button></section>
  <p className="traffic-coverage" role="note">{capture?.mode === "windows-native" ? "Host traffic only · LAN-wide monitoring requires gateway/SPAN/TAP or additional sensors" : capture?.mode === "sensor-required" ? "Capture sensor required · Windows discovery remains available" : "Limited sensor visibility · full LAN coverage is not verified"} · Details in Diagnostics</p><section className={`traffic-map-surface${view === "topology" ? " topology-surface" : ""}`}>
   {view === "topology" ? <>
   <div className="map-meta"><span>{hostCount} {hostCount === 1 ? "host" : "hosts"} · {endpointCount} remote {endpointCount === 1 ? "endpoint" : "endpoints"}{inventory.error ? ` · ${inventory.error} Showing hosts seen in traffic.` : inventory.truncated ? ` · First ${inventory.devices.length} of ${inventory.total} devices` : ""}</span><span>{snapshot?.truncated ? "PARTIAL SNAPSHOT · " : ""}{error && snapshot ? "LAST SNAPSHOT · " : ""}{snapshot ? date(snapshot.generatedAt) : ""}</span></div>
   <TopologyView topology={topology} paused={paused || !!error} panelOpen={!!selectedNodeID} selectedNodeId={selectedNodeID} selectedFlowId={selectedID} onSelectNode={setSelectedNodeID}/>
   {topologyMessage && <div className={`traffic-empty${topology.nodes.length ? " topology-notice" : ""}`} role="status"><strong>{topologyMessage}</strong>{error ? <span>{hints[error.code] ?? "Check your NERVE session and API connection."}</span> : !topology.nodes.length && !inventory.devices.length ? <span>Run a discovery scan on <Link href="/devices">Devices</Link> or connect a traffic sensor.</span> : null}</div>}
   <div className="map-footer topology-legend"><span><i className="legend-out"/>Outbound <i className="legend-in"/>Inbound · measured flows; pulse density follows throughput, motion is illustrative</span><span><i className="status-dot" data-status="online"/>Online <i className="status-dot" data-status="warning"/>Warning <i className="status-dot" data-status="offline"/>Offline · plates are derived /24 groups, not verified VLANs · local device-to-device links not observed · pins are approximate</span></div>
   {selectedNodeID && <TopologyInspector node={topology.nodes.find(n => n.id === selectedNodeID)} flows={flowIndex} stale={!!error} speed={speed} date={date} onSelectFlow={setSelectedID} onClose={() => setSelectedNodeID(null)}/>}
   </> : <>
   <div className="map-meta"><span>{origin ? `LOCAL SITE ${origin.lat.toFixed(3)}°, ${origin.lon.toFixed(3)}° · ${origin.source === "manual" ? "USER SET" : "BROWSER ESTIMATE"}` : "LOCAL SITE NOT SET"}</span><span>{snapshot?.truncated ? "PARTIAL SNAPSHOT · " : ""}{error && snapshot ? "LAST SNAPSHOT · " : ""}{snapshot ? date(snapshot.generatedAt) : ""}</span></div>
   <MapCanvas flows={flows} origin={origin} paused={paused || !!error} onSelect={f => setSelectedID(f.id)}/>
   {message && <div className="traffic-empty" role="status"><strong>{message}</strong>{error && <span>{hints[error.code] ?? "Check your NERVE session and API connection."}</span>}{!origin && !error && <button className="button" onClick={() => setSiteOpen(true)}>Set local site</button>}</div>}
   <div className="map-footer"><span><i className="legend-out"/>Outbound <i className="legend-in"/>Inbound · {capture?.mode === "windows-native" ? "observed byte direction" : "session initiator"}</span><span>Natural Earth · approximate IP locations · {<><a href="https://db-ip.com" target="_blank" rel="noreferrer">IP Geolocation by DB-IP</a> · </>} {snapshot?.summary.unlocatedFlows ?? 0} unlocated</span></div>
   </>}
   {siteOpen && <form className="map-panel site-panel" onSubmit={e => { e.preventDefault(); if (!latitude.trim() || !longitude.trim()) { setLocationError("Enter both coordinates."); return; } saveLocation({ lat: Number(latitude), lon: Number(longitude) }); }}><button type="button" className="inspector-close" aria-label="Close location settings" onClick={() => setSiteOpen(false)}><X/></button><h2>Local site location</h2><p>Coordinates stay in this browser. No default location is assumed. Browser location is requested only when you press Browser.</p>{siteNotice && <p role="status">{siteNotice}</p>}<label>Latitude<input type="number" step="any" min="-90" max="90" required value={latitude} onChange={e => setLatitude(e.target.value)}/></label><label>Longitude<input type="number" step="any" min="-180" max="180" required value={longitude} onChange={e => setLongitude(e.target.value)}/></label><div className="traffic-actions"><button className="button" type="submit">Save location</button><button className="button" type="button" onClick={useLocation}><LocateFixed size={14}/>Browser</button></div>{(origin || siteNotice) && <button className="button" type="button" onClick={() => { setOrigin(null); setSiteNotice(""); setLatitude(""); setLongitude(""); try { localStorage.setItem(siteKey, JSON.stringify({version: 2, source: "unset"})); localStorage.removeItem(legacySiteKey); } catch { setLocationError("Could not clear browser storage."); } }}>Clear location</button>}{locationError && <p role="alert">{locationError}</p>}</form>}
   {diagnostics && <aside className="map-panel diagnostics-panel"><button className="inspector-close" aria-label="Close diagnostics" onClick={() => setDiagnostics(false)}><X/></button><h2>Traffic source</h2><dl><Row label="Status" value={status}/><Row label="Local site source" value={origin ? origin.source === "manual" ? "User confirmed coordinates (accuracy unspecified)" : `Browser estimate (accuracy ±${Math.round(origin.accuracyMeters ?? 0)} m)` : "Local site not set"}/>{origin && <Row label="Location saved" value={date(origin.savedAt)}/>}<Row label="Capture mode" value={capture?.mode === "windows-native" ? "Windows native sensor" : capture?.mode ?? "Unknown"}/><Row label="Interface" value={capture ? `${capture.interfaceName ?? "ID"} (${capture.interfaceId})` : "Unknown"}/><Row label="ETW events lost" value={String(capture?.eventsLost ?? 0)}/>{capture?.backendStage && <Row label="Backend diagnostic" value={`${capture.backendStage} / 0x${((capture.backendError ?? 0) >>> 0).toString(16)}`}/>}<Row label="Code" value={error?.code ?? (snapshot?.summary.activeFlows === 0 ? "no_active_external_flows" : snapshot ? "connected" : "connecting")}/><Row label="Last success" value={snapshot ? date(snapshot.generatedAt) : "—"}/><Row label="Sampled / source flows" value={`${snapshot?.sampledFlows ?? 0} / ${snapshot?.sourceTotalFlows ?? 0}`}/><Row label="Unlocated external flows" value={String(snapshot?.summary.unlocatedFlows ?? 0)}/>{view === "topology" && <Row label="Inventory devices" value={inventory.error ?? (inventory.truncated ? `${inventory.devices.length} of ${inventory.total}` : String(inventory.devices.length))}/>}</dl><p>Arcs visualize connections, not physical routing paths. Remote IP locations are approximate city/region estimates; country representatives indicate only a country, never a verified server location.</p><p>{capture?.visibility ?? "Capture coverage is unknown. A connected source does not imply full LAN monitoring."}</p><p>{error ? hints[error.code] : capture?.mode === "windows-native" ? "Source: Windows ETW. Direction is observed send/receive; byte counts cover TCP/UDP transport events. No payloads are stored." : "Source: ntopng REST v2. Credentials remain on the API server."}</p>{snapshot?.truncated && <p>Partial snapshot: up to 5,000 source flows and 500 displayed groups. Totals refer to sampled flows.</p>}</aside>}
   {selectedID && <aside className="traffic-inspector map-panel"><button className="inspector-close" aria-label="Close flow details" onClick={() => setSelectedID(null)}><X/></button>{selected ? <><span className="eyebrow">{selected.flowCount} flow(s){error ? " · stale" : ""}</span><h2>{selected.remoteCountryName ?? "Unknown location"}</h2><dl><Row label="Remote IP" value={selected.remoteIp}/><Row label="Approximate location" value={[selected.remoteCity, selected.remoteRegion, selected.remoteCountryName].filter(Boolean).join(", ") || "Unknown"}/><Row label="Location source" value={selected.remoteLocationSource === "dbip-city-lite" ? "DB-IP City Lite (approximate)" : selected.remoteLocationSource === "country-representative" ? "Country only · representative on land" : "Unlocated"}/><Row label="Local IP" value={selected.localIp}/><Row label="Local ports" value={selected.localPorts.join(", ")}/><Row label="Remote ports" value={selected.remotePorts.join(", ")}/><Row label="Application / protocol" value={`${selected.application} / ${selected.protocol}`}/><Row label="Throughput" value={speed(selected.bytesPerSecond)}/><Row label="Bytes" value={selected.bytes.toLocaleString()}/><Row label="Direction" value={selected.direction}/><Row label="First / last seen" value={`${date(selected.firstSeenAt)} / ${date(selected.lastSeenAt)}`}/></dl></> : <p>Flow is no longer active.</p>}</aside>}
  </section>
  {matching.length > 0 && <details className="traffic-flow-list"><summary>Inspect {matching.length} flow groups · includes unlocated flows</summary><div>{matching.map(f => <button className="button" key={f.id} onClick={() => setSelectedID(f.id)}>{f.localIp} ↔ {f.remoteIp} · {f.application} · {speed(f.bytesPerSecond)}</button>)}</div></details>}
 </div>;
}
function Stat({ label, value }: { label: string; value: string }) { return <div><span>{label}</span><strong>{value}</strong></div>; }
function Row({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
