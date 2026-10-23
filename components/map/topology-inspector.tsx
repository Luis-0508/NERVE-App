"use client";

import Link from "next/link";
import { X } from "lucide-react";
import { DeviceStatus } from "@/components/devices/device-status";
import type { TrafficFlow } from "@/lib/types";
import { roleLabels, type TopologyNode } from "./topology";

type Props = { node: TopologyNode | undefined; flows: Map<string, TrafficFlow>; stale: boolean; speed: (bytes: number) => string; date: (value: string) => string; onSelectFlow: (id: string) => void; onClose: () => void };

export function TopologyInspector({ node, flows, stale, speed, date, onSelectFlow, onClose }: Props) {
  const nodeFlows = (node?.flowIds ?? []).map(id => flows.get(id)).filter((f): f is TrafficFlow => !!f).sort((a, b) => b.bytesPerSecond - a.bytesPerSecond);
  const device = node?.device;
  return <aside className="map-panel topology-inspector" aria-label="Selected node">
    <button className="inspector-close" aria-label="Close node details" onClick={onClose}><X/></button>
    {!node ? <p>This node is no longer part of the map.</p> : <>
      <span className="eyebrow">{node.kind === "remote" ? "Remote endpoint" : node.kind === "observed" ? "Seen in traffic" : roleLabels[node.role]}{stale ? " · stale" : ""}</span>
      <h2>{node.label}</h2>
      {device && <DeviceStatus status={device.status}/>}
      <dl>
        {node.kind !== "remote" && <Row label="IP address" value={node.ip ?? "—"}/>}
        {device && device.ipAddresses.length > 1 && <Row label="Other current addresses" value={device.ipAddresses.filter(ip => ip !== node.ip).join(", ")}/>}
        {device && <>
          {device.hostname && <Row label="Hostname" value={device.hostname}/>}
          {device.primaryMac && <Row label="MAC" value={device.primaryMac}/>}
          <Row label="Classification" value={device.classification}/>
          {device.vendor && <Row label="Vendor" value={device.vendor}/>}
          {device.operatingSystem && <Row label="Operating system" value={device.operatingSystem}/>}
          <Row label="Last seen" value={date(device.lastSeenAt)}/>
        </>}
        {node.kind === "observed" && <Row label="Inventory" value="No device with this current address"/>}
        {node.kind === "remote" && <>
          <Row label="Approximate location" value={[node.city, node.countryName].filter(Boolean).join(", ") || "Unknown"}/>
          <Row label="Location source" value={nodeFlows[0]?.remoteLocationSource === "dbip-city-lite" ? "DB-IP City Lite (approximate)" : nodeFlows[0]?.remoteLocationSource === "country-representative" ? "Country only" : "Unlocated"}/>
        </>}
        <Row label="Measured throughput" value={nodeFlows.length ? speed(node.bytesPerSecond) : "No matching traffic"}/>
      </dl>
      {nodeFlows.length > 0 && <div className="topology-flows">
        <h3>{nodeFlows.length === 1 ? "1 flow group" : `${nodeFlows.length} flow groups`}</h3>
        <ul>{nodeFlows.slice(0, 8).map(f => <li key={f.id}><button type="button" onClick={() => onSelectFlow(f.id)}>
          <span>{node.kind === "remote" ? f.localIp : f.remoteIp}</span><span>{f.application} · {f.direction === "inbound" ? "↓" : "↑"} {speed(f.bytesPerSecond)}</span>
        </button></li>)}</ul>
        {nodeFlows.length > 8 && <p>{nodeFlows.length - 8} more in the flow list below the map.</p>}
      </div>}
      {device && <Link className="button" href={`/devices/detail?id=${encodeURIComponent(device.id)}`}>Open device</Link>}
    </>}
  </aside>;
}
function Row({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
