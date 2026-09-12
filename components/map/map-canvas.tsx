"use client";

import { memo, useEffect, useRef, useState } from "react";
import { feature, mesh } from "topojson-client";
import type { Topology, GeometryCollection } from "topojson-specification";
import type { TrafficFlow } from "@/lib/types";
import { arcPoints, geographyPath, graticulePath, HEIGHT, Point, project, WIDTH } from "./geometry";

type Geography = { land: string; borders: string };
let geography: Promise<Geography> | undefined;
function loadGeography() {
 return geography ??= fetch("/maps/countries-50m.json").then(r => { if (!r.ok) throw new Error("Map data unavailable"); return r.json(); }).then((world: Topology<{ countries: GeometryCollection }>) => ({
  land: geographyPath(feature(world, world.objects.countries)) ?? "",
  borders: geographyPath(mesh(world, world.objects.countries, (a, b) => a !== b)) ?? "",
 })).catch(e => { geography = undefined; throw e; });
}
const WorldLand = memo(function WorldLand() {
 const [data, setData] = useState<Geography>();
 const [failed, setFailed] = useState(false);
 useEffect(() => { let active = true; loadGeography().then(v => { if (active) setData(v); }).catch(() => { if (active) setFailed(true); }); return () => { active = false; }; }, []);
 return <svg className="world-map" viewBox={`0 0 ${WIDTH} ${HEIGHT}`} aria-label="Natural Earth world map">
  <path d={geographyPath({ type: "Sphere" })!} className="map-ocean" />
  <path d={graticulePath} className="map-grid" />
  {data && <g><path d={data.land} className="map-land"/><path d={data.borders} className="map-borders"/></g>}
  {failed && <text x="600" y="320" textAnchor="middle" fill="currentColor">Local map data unavailable — reload to retry</text>}
 </svg>;
});
type Arc = { flow: TrafficFlow; points: [number, number][]; path: Path2D; opacity: number; exiting: boolean; phase: number };
export function MapCanvas({ flows, origin, paused, onSelect }: { flows: TrafficFlow[]; origin: Point | null; paused: boolean; onSelect: (flow: TrafficFlow) => void }) {
 const canvas = useRef<HTMLCanvasElement>(null);
 const arcs = useRef(new Map<string, Arc>());
 const previousOrigin = useRef("");
 const originRef = useRef(origin), pausedRef = useRef(paused);
 useEffect(() => { originRef.current = origin; pausedRef.current = paused; }, [origin, paused]);
 useEffect(() => {
  const key = JSON.stringify(origin);
  if (previousOrigin.current !== key) { arcs.current.clear(); previousOrigin.current = key; }
  const ids = new Set(flows.map(f => f.id));
  for (const [id, arc] of arcs.current) { arc.exiting = !ids.has(id) || !origin; }
  if (!origin) return;
  for (const flow of flows) {
   const existing = arcs.current.get(flow.id);
   if (existing && existing.flow.remoteLatitude === flow.remoteLatitude && existing.flow.remoteLongitude === flow.remoteLongitude && existing.flow.direction === flow.direction) { existing.flow = flow; existing.exiting = false; continue; }
   const target = { lat: flow.remoteLatitude!, lon: flow.remoteLongitude! };
   const points = flow.direction === "inbound" ? arcPoints(target, origin) : arcPoints(origin, target);
   const path = new Path2D();
   points.forEach(([x, y], i) => { if (!i || Math.abs(x - points[i - 1][0]) > WIDTH / 2) path.moveTo(x, y); else path.lineTo(x, y); });
   arcs.current.set(flow.id, { flow, points, path, opacity: 0, exiting: false, phase: (parseInt(flow.id.slice(0, 6), 16) || 0) % 1000 / 1000 });
  }
 }, [flows, origin]);
 useEffect(() => {
  const element = canvas.current!, context = element.getContext("2d")!;
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  let frame = 0, last = 0, elapsed = 0;
  const draw = (now: number) => {
   frame = requestAnimationFrame(draw);
   if (now - last < 32) return;
   const dt = Math.min((now - last) / 1000, .1); last = now;
   if (document.hidden) return;
   if (!pausedRef.current && !reduced.matches) elapsed += dt;
   const rect = element.getBoundingClientRect(), dpr = Math.min(window.devicePixelRatio || 1, 2);
   const w = Math.round(rect.width * dpr), h = Math.round(rect.height * dpr);
   if (element.width !== w || element.height !== h) { element.width = w; element.height = h; }
   context.setTransform(w / WIDTH, 0, 0, h / HEIGHT, 0, 0); context.clearRect(0, 0, WIDTH, HEIGHT);
   const colors = getComputedStyle(element), inbound = colors.getPropertyValue("--info").trim(), outbound = colors.getPropertyValue("--accent").trim();
   for (const [id, arc] of arcs.current) {
    arc.opacity = Math.max(0, Math.min(1, arc.opacity + dt * (arc.exiting ? -1.2 : 1.8)));
    if (arc.exiting && arc.opacity === 0) { arcs.current.delete(id); continue; }
    const color = arc.flow.direction === "inbound" ? inbound : outbound;
    context.strokeStyle = color; context.fillStyle = color; context.lineCap = "round";
    context.globalAlpha = arc.opacity * .42;
    context.lineWidth = Math.min(3.4, .6 + Math.log10(arc.flow.bytesPerSecond + 1) * .38);
    context.stroke(arc.path);
    const target = project({ lat: arc.flow.remoteLatitude!, lon: arc.flow.remoteLongitude! });
    context.globalAlpha = arc.opacity * .8;
    context.beginPath(); context.arc(target[0], target[1], 2.3, 0, Math.PI * 2); context.fill();
    // A moving head and short tail make direction unambiguous, without a dashed-line illusion.
    const t = ((elapsed / 3.8 + arc.phase) % 1) * 63;
    const i = Math.floor(t), fraction = t - i, a = arc.points[i], b = arc.points[i + 1];
    if (Math.abs(b[0] - a[0]) < WIDTH / 2) {
     const x = a[0] + (b[0] - a[0]) * fraction, y = a[1] + (b[1] - a[1]) * fraction;
     const angle = Math.atan2(b[1] - a[1], b[0] - a[0]);
     context.globalAlpha = arc.opacity; context.lineWidth = 2;
     context.beginPath(); context.moveTo(x - Math.cos(angle) * 9, y - Math.sin(angle) * 9); context.lineTo(x, y); context.stroke();
     context.beginPath(); context.arc(x, y, 2, 0, Math.PI * 2); context.fill();
    }
   }
   if (originRef.current) {
    const [x, y] = project(originRef.current);
    context.globalAlpha = 1; context.fillStyle = outbound; context.strokeStyle = outbound;
    context.beginPath(); context.arc(x, y, 4, 0, Math.PI * 2); context.fill();
    context.globalAlpha = .4; context.lineWidth = 1; context.beginPath(); context.arc(x, y, 9, 0, Math.PI * 2); context.stroke();
   }
  };
  frame = requestAnimationFrame(draw);
  return () => cancelAnimationFrame(frame);
 }, []);
 return <div className="map-drawing"><WorldLand/><canvas ref={canvas} aria-label="Animated external traffic; select a destination for details" onClick={e => {
  const rect = e.currentTarget.getBoundingClientRect(), x = (e.clientX - rect.left) / rect.width * WIDTH, y = (e.clientY - rect.top) / rect.height * HEIGHT;
  let closest: TrafficFlow | undefined, distance = 16;
  for (const arc of arcs.current.values()) {
   if (arc.exiting) continue;
   const p = project({ lat: arc.flow.remoteLatitude!, lon: arc.flow.remoteLongitude! }); const d = Math.hypot(p[0] - x, p[1] - y);
   if (d < distance) { distance = d; closest = arc.flow; }
  }
  if (closest) onSelect(closest);
 }}/></div>;
}
