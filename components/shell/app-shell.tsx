"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { CoreStatus } from "./core-status";
import { ThemeControl } from "./theme-control";

// Only sections that exist are listed; planned modules appear once they ship.
const nav = [
  { id: "overview", label: "Overview", href: "/" },
  { id: "devices", label: "Devices", href: "/devices" },
  { id: "map", label: "Traffic map", href: "/map" },
  { id: "settings", label: "Settings", href: "/settings" },
] as const;

export function AppShell({ active, children }: { active: string; children: ReactNode }) {
  const router = useRouter();
  const input = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");

  // "/" jumps to the device search from anywhere outside a text field.
  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      const target = event.target as HTMLElement | null;
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      event.preventDefault();
      input.current?.focus();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  function jump(event: FormEvent) {
    event.preventDefault();
    const value = query.trim();
    if (!value) return;
    router.push(`/devices?search=${encodeURIComponent(value)}`);
  }

  return (
    <div className="app-shell">
      <header className="shell-bar">
        <Link className="brand" href="/" aria-label="NERVE overview">
          <svg className="brand-mark" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2.5l8.2 4.75v9.5L12 21.5l-8.2-4.75v-9.5z" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" /><circle cx="12" cy="12" r="2.6" fill="currentColor" /></svg>
          <span className="brand-name">NERVE</span>
        </Link>
        <nav className="nav" aria-label="Primary navigation">
          {nav.map((item) => (
            <Link key={item.id} className="nav-item" href={item.href} data-active={active === item.id} aria-current={active === item.id ? "page" : undefined}>{item.label}</Link>
          ))}
        </nav>
        <div className="shell-tools">
          <form className="jump-search" role="search" onSubmit={jump}>
            <label className="sr-only" htmlFor="jump-search">Find a device by name, IP, MAC or tag</label>
            <input id="jump-search" ref={input} type="search" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Find device: IP, host, MAC" autoComplete="off" />
            <kbd aria-hidden="true">/</kbd>
          </form>
          <CoreStatus />
          <ThemeControl />
        </div>
      </header>
      <main className="main">{children}</main>
    </div>
  );
}
