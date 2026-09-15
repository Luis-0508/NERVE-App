"use client";

import Link from "next/link";
import { Activity, Boxes, Cable, CircleDot, Container, Gauge, Hexagon, Map, Network, ServerCog, Settings, ShieldCheck } from "lucide-react";
import type { ReactNode } from "react";
import { CoreStatus } from "./core-status";
import { ThemeControl } from "./theme-control";

const nav = [
  { id: "overview", label: "Overview", href: "/", icon: Gauge, enabled: true },
  { id: "map", label: "Map", href: "/map", icon: Map, enabled: true },
  { id: "devices", label: "Devices", href: "/devices", icon: Network, enabled: true },
  { id: "infrastructure", label: "Infrastructure", href: "#", icon: ServerCog, enabled: false },
  { id: "services", label: "Services", href: "#", icon: Boxes, enabled: false },
  { id: "traffic", label: "Traffic", href: "#", icon: Cable, enabled: false },
  { id: "security", label: "Security", href: "#", icon: ShieldCheck, enabled: false },
  { id: "events", label: "Events", href: "#", icon: Activity, enabled: false },
  { id: "settings", label: "Settings", href: "/settings", icon: Settings, enabled: true },
] as const;

export function AppShell({ active, children }: { active: string; children: ReactNode }) {
  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="Primary navigation">
        <div className="brand">
          <Hexagon className="brand-mark" aria-hidden="true" />
          <span className="brand-name">NERVE</span>
          <span className="brand-version">0.1</span>
        </div>
        <nav className="nav">
          {nav.map((item, index) => {
            const Icon = item.icon;
            const className = `nav-item${index === nav.length - 1 ? " nav-last" : ""}`;
            if (!item.enabled) {
              return (
                <span key={item.id} className={className} aria-disabled="true" title={`${item.label} is planned for a later milestone`}>
                  <Icon aria-hidden="true" /><span>{item.label}</span>
                </span>
              );
            }
            return (
              <Link key={item.id} className={className} href={item.href} data-active={active === item.id} aria-current={active === item.id ? "page" : undefined}>
                <Icon aria-hidden="true" /><span>{item.label}</span>
              </Link>
            );
          })}
        </nav>
        <div className="sidebar-footer"><Container aria-hidden="true" size={13} /> Self-hosted control plane</div>
      </aside>
      <main className="main">
        <header className="topbar">
          <CircleDot size={15} aria-hidden="true" />
          <span className="topbar-title">Local network</span>
          <div className="topbar-spacer" />
          <CoreStatus />
          <ThemeControl />
        </header>
        {children}
      </main>
    </div>
  );
}
