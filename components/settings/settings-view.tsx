"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { AgentsSettings } from "@/components/remote/agents-settings";
import { api } from "@/lib/api";
import type { ServiceStatus, Session } from "@/lib/types";

export function SettingsView() {
  const [status, setStatus] = useState<ServiceStatus | null>(null);
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState(false);
  useEffect(() => {
    api.status().then(setStatus).catch(() => setError(true));
    api.session().then(setSession).catch(() => setSession(null));
  }, []);
  const permissions = session?.user?.permissions ?? [];
  return <div className="page"><div className="page-heading"><div><h1>Settings</h1><p>Installation state and administrative entry points.</p></div></div><section className="settings-panel"><div className="settings-row"><div><h2>Core API</h2><p>Versioned HTTP endpoint used by the web application.</p></div><div className="setting-value mono">{error ? "Unavailable" : status ? `${status.version} · Online` : "Checking…"}</div></div><div className="settings-row"><div><h2>Database</h2><p>PostgreSQL readiness reported by the core service.</p></div><div className="setting-value mono">{status?.database.status ?? "Unknown"}</div></div><div className="settings-row"><div><h2>Authentication</h2><p>Initialize a new installation or sign in to an existing one.</p></div><div className="setting-value"><Link className="button" href="/setup">Initialize</Link> <Link className="button" href="/login">Sign in</Link></div></div></section>
    {permissions.includes("agents.view") && <AgentsSettings permissions={permissions} />}
  </div>;
}
