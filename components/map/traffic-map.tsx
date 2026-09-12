"use client";

import { LocateFixed, Pause, Play, RefreshCw, Search, X, Settings2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { TrafficFlow, TrafficSnapshot } from "@/lib/types";
import { MapCanvas } from "./map-canvas";
import { Point, validPoint } from "./geometry";

const storedOrigin = "nerve:traffic-map-origin";
function speed(bytes: number) { const units = ["bit/s", "Kbit/s", "Mbit/s", "Gbit/s"]; let value = bytes * 8, unit = 0; while (value >= 1000 && unit < 3) { value /= 1000; unit++; } return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`; }
function date(value: string) { const d = new Date(value); return !Number.isFinite(d.getTime()) || d.getUTCFullYear() < 1970 ? "—" : d.toLocaleString(); }
const hints: Record<string, string> = {
 traffic_source_not_configured: "Configure ntopng in .env",
 traffic_source_authentication_failed: "Check ntopng username, password and interface permissions.",
 traffic_source_unreachable: "Check URL, Docker connectivity, TLS certificate and timeout.",
 traffic_source_invalid_response: "Check the ntopng REST v2 endpoint and interface ID.",
 traffic_source_invalid_configuration: "Use an http(s) base URL and a non-negative interface ID.",
};
export function TrafficMap() {
 const [snapshot, setSnapshot] = useState<TrafficSnapshot | null>(null), [error, setError] = useState<{ message: string; code: string } | null>(null), [paused, setPaused] = useState(false), [loading, setLoading] = useState(false);
 const [direction, setDirection] = useState("all"), [application, setApplication] = useState("all"), [protocol, setProtocol] = useState("all"), [search, setSearch] = useState(""), [local, setLocal] = useState("all");
 const [origin, setOrigin] = useState<Point | null>(null), [siteOpen, setSiteOpen] = useState(false), [latitude, setLatitude] = useState(""), [longitude, setLongitude] = useState(""), [locationError, setLocationError] = useState("");
 const [selectedID, setSelectedID] = useState<string | null>(null), [diagnostics, setDiagnostics] = useState(false);
 const requestActive = useRef(false), mounted = useRef(false);
 useEffect(() => {
  mounted.current = true;
  try { const raw = localStorage.getItem(storedOrigin); if (raw) { const value = JSON.parse(raw); if (value && validPoint(value)) { setOrigin(value); setLatitude(String(value.lat)); setLongitude(String(value.lon)); } } } catch { /* Storage can be disabled. Manual location still works for this visit. */ }
  return () => { mounted.current = false; };
 }, []);
 const load = useCallback(async () => {
  if (requestActive.current) return;
  requestActive.current = true; setLoading(true);
  try { const data = await api.liveTraffic(); if (mounted.current) { setSnapshot(data); setError(null); } }
  catch (e) { if (mounted.current) setError({ message: e instanceof APIError ? e.message : "NERVE API unreachable", code: e instanceof APIError ? e.code ?? "api_error" : "api_unreachable" }); }
  finally { requestActive.current = false; if (mounted.current) setLoading(false); }
 }, []);
 useEffect(() => {
  if (paused) return;
  void load();
  const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 2000);
  return () => window.clearInterval(timer);
 }, [load, paused]);
 function saveLocation(point: Point) {
  if (!validPoint(point)) { setLocationError("Latitude must be −90…90; longitude −180…180."); return; }
  setOrigin(point); setLatitude(String(point.lat)); setLongitude(String(point.lon)); setLocationError("");
  try { localStorage.setItem(storedOrigin, JSON.stringify(point)); setSiteOpen(false); } catch { setLocationError("Location set for this visit; browser storage is unavailable."); }
 }
 function useLocation() {
  if (!navigator.geolocation) { setLocationError("Browser geolocation is unavailable. Enter coordinates manually."); return; }
  navigator.geolocation.getCurrentPosition(p => saveLocation({ lat: p.coords.latitude, lon: p.coords.longitude }), () => setLocationError("Browser location unavailable or denied. Enter coordinates manually."), { maximumAge: 86_400_000, timeout: 10_000 });
 }
 const applications = useMemo(() => [...new Set(snapshot?.flows.map(f => f.application) ?? [])].sort(), [snapshot]);
 const protocols = useMemo(() => [...new Set(snapshot?.flows.map(f => f.protocol) ?? [])].sort(), [snapshot]);
 const localIPs = useMemo(() => [...new Set(snapshot?.flows.map(f => f.localIp) ?? [])].sort(), [snapshot]);
 const matching = useMemo(() => (snapshot?.flows ?? []).filter(f => (direction === "all" || f.direction === direction) && (application === "all" || f.application === application) && (protocol === "all" || f.protocol === protocol) && (local === "all" || f.localIp === local) && `${f.localIp} ${f.remoteIp} ${f.remoteCountryName ?? ""} ${f.application}`.toLowerCase().includes(search.toLowerCase())), [snapshot, direction, application, protocol, local, search]);
 const flows = useMemo(() => error ? [] : matching.filter(f => f.remoteLatitude !== undefined && f.remoteLongitude !== undefined && validPoint({ lat: f.remoteLatitude, lon: f.remoteLongitude })), [matching, error]);
 const selected = snapshot?.flows.find(f => f.id === selectedID);
 const status = error?.message ?? (!snapshot ? "Connecting to ntopng…" : snapshot.summary.activeFlows === 0 ? "No active external flows" : paused ? "Paused" : "Connected");
 const countries = new Set(flows.map(f => f.remoteCountryCode)).size;
 const message = error?.message ?? (!snapshot ? "Connecting to ntopng…" : !snapshot.summary.activeFlows ? "No active external flows" : !origin ? "Set your local site location" : !matching.length ? "No traffic matches the active filters" : !flows.length ? "Active flows have no geographic location" : null);
 return <div className="page traffic-page">
  <header className="traffic-header"><div><div className="eyebrow"><span className={!paused && !error && snapshot ? "live-dot" : "live-dot paused"}/>{error ? "SOURCE OFFLINE" : paused ? "PAUSED" : snapshot ? "LIVE TRAFFIC" : "CONNECTING"}</div><h1>Traffic Map</h1></div><div className="traffic-actions"><button className="button" onClick={() => setPaused(v => !v)}>{paused ? <Play/> : <Pause/>}{paused ? "Resume" : "Pause"}</button><button className="button" disabled={loading} onClick={() => void load()}><RefreshCw/>{loading ? "Checking…" : "Refresh"}</button><button className="button" aria-expanded={diagnostics} onClick={() => setDiagnostics(v => !v)}>Diagnostics</button></div></header>
  <section className={`traffic-stats${error ? " stale" : ""}`} aria-label={error ? "Last successful snapshot" : "Traffic statistics"}><Stat label="Active flows" value={String(snapshot?.summary.activeFlows ?? 0)}/><Stat label="Throughput" value={speed(snapshot?.summary.bytesPerSecond ?? 0)}/><Stat label="↓ Inbound sessions" value={speed(snapshot?.summary.inboundBytesPerSecond ?? 0)}/><Stat label="↑ Outbound sessions" value={speed(snapshot?.summary.outboundBytesPerSecond ?? 0)}/><Stat label="Mapped / countries" value={`${flows.length} / ${countries}`}/></section>
  <section className="traffic-controls"><div className="segmented">{["all", "inbound", "outbound"].map(item => <button key={item} onClick={() => setDirection(item)} data-active={direction === item}>{item === "all" ? "All traffic" : item === "inbound" ? "↓ Inbound" : "↑ Outbound"}</button>)}</div><select aria-label="Application" value={application} onChange={e => setApplication(e.target.value)}><option value="all">All applications</option>{[...new Set([...applications, ...(application !== "all" ? [application] : [])])].map(v => <option key={v}>{v}</option>)}</select><select aria-label="Protocol" value={protocol} onChange={e => setProtocol(e.target.value)}><option value="all">All protocols</option>{[...new Set([...protocols, ...(protocol !== "all" ? [protocol] : [])])].map(v => <option key={v}>{v}</option>)}</select><select aria-label="Local device" value={local} onChange={e => setLocal(e.target.value)}><option value="all">All local devices</option>{[...new Set([...localIPs, ...(local !== "all" ? [local] : [])])].map(v => <option key={v}>{v}</option>)}</select><label className="traffic-search"><Search/><input aria-label="Search flows" value={search} onChange={e => setSearch(e.target.value)} placeholder="Country, application or IP"/></label><button className="button" onClick={() => setSiteOpen(v => !v)} aria-expanded={siteOpen}><Settings2 size={14}/>Local site</button></section>
  <section className="traffic-map-surface">
   <div className="map-meta"><span>{origin ? `LOCAL SITE ${origin.lat.toFixed(3)}°, ${origin.lon.toFixed(3)}°` : "LOCAL SITE NOT SET"}</span><span>{snapshot?.truncated ? "PARTIAL SNAPSHOT · " : ""}{error && snapshot ? "LAST SNAPSHOT · " : ""}{snapshot ? date(snapshot.generatedAt) : ""}</span></div>
   <MapCanvas flows={flows} origin={origin} paused={paused || !!error} onSelect={f => setSelectedID(f.id)}/>
   {message && <div className="traffic-empty" role="status"><strong>{message}</strong>{error && <span>{hints[error.code] ?? "Check your NERVE session and API connection."}</span>}{!origin && !error && <button className="button" onClick={() => setSiteOpen(true)}>Set local site</button>}</div>}
   <div className="map-footer"><span><i className="legend-out"/>Outbound <i className="legend-in"/>Inbound · session initiator</span><span>Natural Earth · country centroids · {snapshot?.summary.unlocatedFlows ?? 0} unlocated</span></div>
   {siteOpen && <form className="map-panel site-panel" onSubmit={e => { e.preventDefault(); if (!latitude.trim() || !longitude.trim()) { setLocationError("Enter both coordinates."); return; } saveLocation({ lat: Number(latitude), lon: Number(longitude) }); }}><button type="button" className="inspector-close" aria-label="Close location settings" onClick={() => setSiteOpen(false)}><X/></button><h2>Local site location</h2><p>Coordinates stay in this browser. No default location is assumed.</p><label>Latitude<input type="number" step="any" min="-90" max="90" required value={latitude} onChange={e => setLatitude(e.target.value)}/></label><label>Longitude<input type="number" step="any" min="-180" max="180" required value={longitude} onChange={e => setLongitude(e.target.value)}/></label><div className="traffic-actions"><button className="button" type="submit">Save location</button><button className="button" type="button" onClick={useLocation}><LocateFixed size={14}/>Browser</button></div>{origin && <button className="button" type="button" onClick={() => { setOrigin(null); setLatitude(""); setLongitude(""); try { localStorage.removeItem(storedOrigin); } catch { setLocationError("Could not clear browser storage."); } }}>Clear location</button>}{locationError && <p role="alert">{locationError}</p>}</form>}
   {diagnostics && <aside className="map-panel diagnostics-panel"><button className="inspector-close" aria-label="Close diagnostics" onClick={() => setDiagnostics(false)}><X/></button><h2>Traffic source</h2><dl><Row label="Status" value={status}/><Row label="Code" value={error?.code ?? (snapshot?.summary.activeFlows === 0 ? "no_active_external_flows" : snapshot ? "connected" : "connecting")}/><Row label="Last success" value={snapshot ? date(snapshot.generatedAt) : "—"}/><Row label="Sampled / source flows" value={`${snapshot?.sampledFlows ?? 0} / ${snapshot?.sourceTotalFlows ?? 0}`}/><Row label="Unlocated external flows" value={String(snapshot?.summary.unlocatedFlows ?? 0)}/></dl><p>{error ? hints[error.code] : "Source: ntopng REST v2. Credentials remain on the API server."}</p>{snapshot?.truncated && <p>Partial snapshot: up to 5,000 source flows and 500 displayed groups. Totals refer to sampled flows.</p>}</aside>}
   {selectedID && <aside className="traffic-inspector map-panel"><button className="inspector-close" aria-label="Close flow details" onClick={() => setSelectedID(null)}><X/></button>{selected ? <><span className="eyebrow">{selected.flowCount} session(s){error ? " · stale" : ""}</span><h2>{selected.remoteCountryName ?? "Unknown location"}</h2><dl><Row label="Remote IP" value={selected.remoteIp}/><Row label="Local IP" value={selected.localIp}/><Row label="Local ports" value={selected.localPorts.join(", ")}/><Row label="Remote ports" value={selected.remotePorts.join(", ")}/><Row label="Application / protocol" value={`${selected.application} / ${selected.protocol}`}/><Row label="Throughput" value={speed(selected.bytesPerSecond)}/><Row label="Bytes" value={selected.bytes.toLocaleString()}/><Row label="Session direction" value={selected.direction}/><Row label="First / last seen" value={`${date(selected.firstSeenAt)} / ${date(selected.lastSeenAt)}`}/></dl></> : <p>Flow is no longer active.</p>}</aside>}
  </section>
  {matching.length > 0 && <details className="traffic-flow-list"><summary>Inspect {matching.length} flow groups · includes unlocated flows</summary><div>{matching.map(f => <button className="button" key={f.id} onClick={() => setSelectedID(f.id)}>{f.localIp} ↔ {f.remoteIp} · {f.application} · {speed(f.bytesPerSecond)}</button>)}</div></details>}
 </div>;
}
function Stat({ label, value }: { label: string; value: string }) { return <div><span>{label}</span><strong>{value}</strong></div>; }
function Row({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
