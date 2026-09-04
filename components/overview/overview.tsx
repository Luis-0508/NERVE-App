"use client";

import Link from "next/link";
import { AlertTriangle, Clock3, RefreshCw, Rows3 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { NerveEvent, ServiceStatus } from "@/lib/types";

function formatUptime(seconds?: number) {
  if (seconds === undefined) return "—";
  if (seconds < 60) return `${seconds}s`;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return hours ? `${hours}h ${minutes}m` : `${minutes}m`;
}

export function Overview() {
  const [status, setStatus] = useState<ServiceStatus | null>(null);
  const [events, setEvents] = useState<NerveEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true); setError(null); setAuthRequired(false);
    try {
      const service = await api.status();
      setStatus(service);
      try { setEvents((await api.events()).items); }
      catch (eventError) {
        if (eventError instanceof APIError && eventError.status === 401) setAuthRequired(true);
        else throw eventError;
      }
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "The core service could not be reached.");
    } finally { setLoading(false); }
  }, []);

  useEffect(() => { void load(); }, [load]);

  return (
    <div className="page">
      <div className="page-heading">
        <div><h1>Overview</h1><p>Current state reported by the NERVE control plane.</p></div>
        <button className="button" type="button" onClick={() => void load()} disabled={loading}><RefreshCw size={13} aria-hidden="true" />Refresh</button>
      </div>

      <section className="summary-strip" aria-label="System summary">
        <div className="summary-item"><span className="summary-label">Core service</span><span className="summary-value"><span className="status-dot" data-status={status ? "online" : error ? "offline" : "unknown"} />{status ? "Online" : error ? "Unavailable" : "Checking"}</span></div>
        <div className="summary-item"><span className="summary-label">Database</span><span className="summary-value"><span className="status-dot" data-status={status?.database.status === "ok" ? "online" : status ? "warning" : "unknown"} />{status?.database.status ?? "Unknown"}</span></div>
        <div className="summary-item"><span className="summary-label">Version</span><span className="summary-value mono">{status?.version ?? "—"}</span></div>
        <div className="summary-item"><span className="summary-label">Uptime</span><span className="summary-value mono">{formatUptime(status?.uptimeSeconds)}</span></div>
      </section>

      <section className="overview-grid">
        <div className="overview-primary">
          <div className="section-head"><h2 className="section-title">Recent events</h2><span className="mono">latest 8</span></div>
          {error ? (
            <div className="error-state"><h3>Core service unavailable</h3><p>{error} Check that the API and PostgreSQL services are running, then retry.</p><button className="button" onClick={() => void load()} type="button">Retry connection</button></div>
          ) : authRequired ? (
            <div className="empty-state"><AlertTriangle aria-hidden="true" /><h3>Authentication required</h3><p>Sign in to read infrastructure events. System health remains visible without a session.</p><p><Link className="button button-primary" href="/login">Sign in</Link></p></div>
          ) : events.length ? (
            <div className="table-wrap"><table className="data-table"><thead><tr><th>Event</th><th>Category</th><th>Severity</th><th>Occurred</th></tr></thead><tbody>{events.map((event) => <tr key={event.id}><td>{event.message}</td><td>{event.category}</td><td>{event.severity}</td><td className="mono">{new Date(event.occurredAt).toLocaleString()}</td></tr>)}</tbody></table></div>
          ) : (
            <div className="empty-state"><Rows3 aria-hidden="true" /><h3>No events recorded</h3><p>Events will appear here after discovery or an integration produces its first observation.</p></div>
          )}
        </div>
        <aside className="overview-aside" aria-label="System details">
          <div className="section-head"><h2 className="section-title">Control plane</h2><Clock3 size={14} aria-hidden="true" /></div>
          <dl className="aside-list">
            <div className="aside-row"><dt>API</dt><dd>/api/v1</dd></div>
            <div className="aside-row"><dt>Database latency</dt><dd>{status?.database.latencyMs !== undefined ? `${status.database.latencyMs} ms` : "—"}</dd></div>
            <div className="aside-row"><dt>Realtime</dt><dd>/api/v1/stream</dd></div>
            <div className="aside-row"><dt>Server time</dt><dd>{status?.time ? new Date(status.time).toLocaleTimeString() : "—"}</dd></div>
          </dl>
          <div className="aside-note"><strong>Foundation milestone</strong>Discovery and integrations are intentionally not simulated. Their navigation remains unavailable until real backend support is added.</div>
        </aside>
      </section>
    </div>
  );
}

