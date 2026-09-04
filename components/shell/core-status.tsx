"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export function CoreStatus() {
  const [online, setOnline] = useState<boolean | null>(null);
  useEffect(() => { api.status().then(() => setOnline(true)).catch(() => setOnline(false)); }, []);
  const status = online === null ? "unknown" : online ? "online" : "offline";
  return <div className="core-state" title={`Core service: ${status}`}><span className="status-dot" data-status={status} /><span>Core {status}</span></div>;
}

