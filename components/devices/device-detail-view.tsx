"use client";

import Link from "next/link";
import { ArrowLeft, Cable, CircleOff, RefreshCw, Server, Waypoints } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceDetail, NerveEvent } from "@/lib/types";
import { DeviceRemotePanel } from "@/components/remote/device-remote-panel";
import { DeviceStatus } from "./device-status";

type Tab = "overview" | "network" | "services" | "remote" | "events" | "relationships" | "notes";
const tabs: Array<{ id: Tab; label: string }> = [
  { id: "overview", label: "Overview" }, { id: "network", label: "Network" },
  { id: "services", label: "Services" }, { id: "remote", label: "Remote" }, { id: "events", label: "Events" },
  { id: "relationships", label: "Relationships" }, { id: "notes", label: "Notes" },
];

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function formatSpeed(value?: number) {
  if (!value) return "—";
  if (value >= 1_000_000_000) return `${value / 1_000_000_000} Gbit/s`;
  if (value >= 1_000_000) return `${value / 1_000_000} Mbit/s`;
  return `${value} bit/s`;
}

function valueOrDash(value?: string) { return value || "—"; }

export function DeviceDetailView() {
  const [deviceID, setDeviceID] = useState<string | null>(null);
  const [detail, setDetail] = useState<DeviceDetail | null>(null);
  const [events, setEvents] = useState<NerveEvent[]>([]);
  const [tab, setTab] = useState<Tab>("overview");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [permissions, setPermissions] = useState<string[]>([]);

  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get("id");
    setDeviceID(id);
    if (!id) setLoading(false);
  }, []);

  const load = useCallback(async () => {
    if (!deviceID) return;
    setLoading(true); setError(null); setAuthRequired(false);
    try {
      const [nextDetail, nextEvents, session] = await Promise.all([api.device(deviceID), api.events(25, deviceID), api.session()]);
      setDetail(nextDetail); setEvents(nextEvents.items); setPermissions(session.user?.permissions ?? []);
    } catch (requestError) {
      if (requestError instanceof APIError && requestError.status === 401) setAuthRequired(true);
      else if (requestError instanceof APIError && requestError.status === 404) setError("The requested device does not exist.");
      else setError(requestError instanceof Error ? requestError.message : "The device could not be loaded.");
    } finally { setLoading(false); }
  }, [deviceID]);

  useEffect(() => { void load(); }, [load]);

  if (!deviceID && !loading) return <MissingDevice message="No device ID was supplied." />;
  if (authRequired) return <MissingDevice message="Sign in to inspect this device." action={<Link className="button button-primary" href="/login">Sign in</Link>} />;
  if (error) return <MissingDevice message={error} action={<button className="button" type="button" onClick={() => void load()}>Retry</button>} />;
  if (!detail) return <DeviceDetailLoading />;

  const device = detail.device;
  return <div className="page device-detail-page">
    <Link className="back-link" href="/devices"><ArrowLeft aria-hidden="true" />Devices</Link>
    <header className="device-detail-header">
      <div className="device-title"><span className="device-kind-icon"><Server aria-hidden="true" /></span><div><h1>{device.name}</h1><div className="device-title-meta"><DeviceStatus status={device.status} /><span>{device.classification}</span><span className="mono">{valueOrDash(device.primaryIpAddress)}</span></div></div></div>
      <button className="button" type="button" onClick={() => void load()} disabled={loading}><RefreshCw size={14} aria-hidden="true" />Refresh</button>
    </header>

    <div className="device-summary-line">
      <span><small>Hostname</small>{valueOrDash(device.hostname)}</span>
      <span><small>Operating system</small>{valueOrDash(device.operatingSystem)}</span>
      <span><small>Vendor</small>{valueOrDash(device.vendor)}</span>
      <span><small>Last seen</small>{formatDate(device.lastSeenAt)}</span>
    </div>

    <div className="detail-tabs" role="tablist" aria-label="Device detail sections">{tabs.map((item) => <button key={item.id} role="tab" type="button" aria-selected={tab === item.id} onClick={() => setTab(item.id)}>{item.label}</button>)}</div>
    <section className="detail-surface" role="tabpanel">
      {tab === "overview" && <OverviewTab detail={detail} />}
      {tab === "network" && <NetworkTab detail={detail} />}
      {tab === "services" && <ServicesTab detail={detail} />}
      {tab === "remote" && <DeviceRemotePanel deviceId={device.id} permissions={permissions} />}
      {tab === "events" && <EventsTab events={events} />}
      {tab === "relationships" && <RelationshipsTab detail={detail} />}
      {tab === "notes" && <NotesTab notes={device.notes} />}
    </section>
  </div>;
}

function OverviewTab({ detail }: { detail: DeviceDetail }) {
  const { device } = detail;
  return <div className="detail-grid"><section><DetailHeading title="Identity" /><dl className="property-list"><Property label="Name" value={device.name} /><Property label="Classification" value={device.classification} /><Property label="Hostname" value={device.hostname} mono /><Property label="Primary MAC" value={device.primaryMac} mono /><Property label="Manufacturer" value={device.vendor} /><Property label="Model" value={device.model} /><Property label="Serial number" value={device.serialNumber} mono /></dl></section><section><DetailHeading title="Inventory" /><dl className="property-list"><Property label="Owner" value={device.owner} /><Property label="Location" value={device.location} /><Property label="First seen" value={formatDate(device.firstSeenAt)} /><Property label="Last seen" value={formatDate(device.lastSeenAt)} /><Property label="Approved" value={device.approvedAt ? formatDate(device.approvedAt) : "No"} /><Property label="Interfaces" value={String(detail.interfaces.length)} /><Property label="Service endpoints" value={String(detail.services.length)} /></dl></section></div>;
}

function NetworkTab({ detail }: { detail: DeviceDetail }) {
  if (!detail.interfaces.length) return <SectionEmpty icon={<Cable />} title="No interfaces recorded" copy="Interface observations will appear here when a source reports them." />;
  return <div><DetailHeading title="Interfaces" /><div className="interface-list">{detail.interfaces.map((item) => <section className="interface-row" key={item.id}><div><strong>{item.name}</strong><span className="mono">{valueOrDash(item.macAddress)}</span></div><dl><Property label="State" value={item.isUp === undefined ? "Unknown" : item.isUp ? "Up" : "Down"} /><Property label="Media" value={item.mediaType} /><Property label="Speed" value={formatSpeed(item.speedBps)} /><Property label="MTU" value={item.mtu ? String(item.mtu) : undefined} /></dl><div className="address-list">{item.addresses.length ? item.addresses.map((address) => <span key={address.id} className="mono">{address.address}{address.isPrimary && <small>Primary</small>}</span>) : <span className="muted">No addresses</span>}</div></section>)}</div>{detail.ipAddresses.some((item) => item.validUntil) && <><DetailHeading title="Address history" /><div className="table-wrap"><table className="data-table"><thead><tr><th>Address</th><th>Interface</th><th>Valid from</th><th>Valid until</th></tr></thead><tbody>{detail.ipAddresses.filter((item) => item.validUntil).map((item) => <tr key={item.id}><td className="mono">{item.address}</td><td>{item.interfaceName}</td><td>{formatDate(item.validFrom)}</td><td>{formatDate(item.validUntil)}</td></tr>)}</tbody></table></div></> }</div>;
}

function ServicesTab({ detail }: { detail: DeviceDetail }) {
  if (!detail.services.length) return <SectionEmpty icon={<CircleOff />} title="No service endpoints recorded" copy="Discovered TCP and UDP endpoints will appear here with observation history." />;
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>Port</th><th>Protocol</th><th>Service</th><th>State</th><th>Exposure</th><th>First seen</th><th>Last seen</th></tr></thead><tbody>{detail.services.map((service) => <tr key={service.id}><td className="mono">{service.port}</td><td>{service.protocol.toUpperCase()}</td><td>{service.name}</td><td>{service.state}</td><td>{service.exposure}</td><td>{formatDate(service.firstSeenAt)}</td><td>{formatDate(service.lastSeenAt)}</td></tr>)}</tbody></table></div>;
}

function EventsTab({ events }: { events: NerveEvent[] }) {
  if (!events.length) return <SectionEmpty icon={<CircleOff />} title="No events recorded" copy="State and identity changes for this device will appear here." />;
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>Event</th><th>Category</th><th>Severity</th><th>Occurred</th></tr></thead><tbody>{events.map((event) => <tr key={event.id}><td>{event.message}</td><td>{event.category}</td><td>{event.severity}</td><td>{formatDate(event.occurredAt)}</td></tr>)}</tbody></table></div>;
}

function RelationshipsTab({ detail }: { detail: DeviceDetail }) {
  if (!detail.relationships.length) return <SectionEmpty icon={<Waypoints />} title="No relationships recorded" copy="Topology relationships will appear when they have evidence and provenance." />;
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>Relationship</th><th>Direction</th><th>Entity</th><th>Kind</th><th>Source</th><th>Confidence</th><th>Last seen</th></tr></thead><tbody>{detail.relationships.map((relationship) => <tr key={relationship.id}><td>{relationship.type}</td><td>{relationship.direction}</td><td>{relationship.relatedEntity.kind === "device" ? <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(relationship.relatedEntity.id)}`}>{relationship.relatedEntity.name}</Link> : relationship.relatedEntity.name}</td><td>{relationship.relatedEntity.kind}</td><td>{relationship.source}</td><td>{relationship.confidence}%{relationship.manuallyConfirmed ? " · Confirmed" : ""}</td><td>{formatDate(relationship.lastSeenAt)}</td></tr>)}</tbody></table></div>;
}

function NotesTab({ notes }: { notes?: string }) {
  return notes ? <div className="device-notes">{notes}</div> : <SectionEmpty icon={<CircleOff />} title="No notes" copy="No administrator notes have been recorded for this device." />;
}

function DetailHeading({ title }: { title: string }) { return <div className="detail-section-heading"><h2>{title}</h2></div>; }
function Property({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{valueOrDash(value)}</dd></div>; }
function SectionEmpty({ icon, title, copy }: { icon: React.ReactNode; title: string; copy: string }) { return <div className="empty-state detail-empty">{icon}<h2>{title}</h2><p>{copy}</p></div>; }
function MissingDevice({ message, action }: { message: string; action?: React.ReactNode }) { return <div className="page"><Link className="back-link" href="/devices"><ArrowLeft aria-hidden="true" />Devices</Link><div className="error-state"><h2>Device unavailable</h2><p>{message}</p>{action}</div></div>; }
function DeviceDetailLoading() { return <div className="page"><span className="skeleton-line skeleton-heading" /><div className="device-summary-line loading-block"><span /><span /><span /><span /></div><div className="detail-surface"><div className="skeleton-detail"><span /><span /><span /><span /></div></div></div>; }
