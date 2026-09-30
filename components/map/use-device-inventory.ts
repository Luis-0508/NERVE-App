"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceSummary } from "@/lib/types";

const PAGE_SIZE = 200, MAX_DEVICES = 1000, REFRESH_MS = 30_000;

// Inventory changes slowly, so it refreshes far less often than live traffic.
export function useDeviceInventory(enabled: boolean) {
  const [devices, setDevices] = useState<DeviceSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const active = useRef(false);
  const load = useCallback(async () => {
    if (active.current) return;
    active.current = true;
    try {
      const items: DeviceSummary[] = [];
      let reported = 0;
      for (let offset = 0; offset < MAX_DEVICES; offset += PAGE_SIZE) {
        const page = await api.devices({ sort: "ip", direction: "asc", limit: PAGE_SIZE, offset });
        items.push(...page.items); reported = page.total;
        if (items.length >= page.total || page.items.length < PAGE_SIZE) break;
      }
      setDevices(items); setTotal(reported); setError(null);
    } catch (e) {
      setError(e instanceof APIError && e.status === 401 ? "Sign in to show inventory devices." : "Device inventory unavailable.");
    } finally { active.current = false; setLoaded(true); }
  }, []);
  useEffect(() => {
    if (!enabled) return;
    void load();
    const timer = window.setInterval(() => { if (!document.hidden) void load(); }, REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [enabled, load]);
  return { devices, total, truncated: total > devices.length, error, loaded };
}
