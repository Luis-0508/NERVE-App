"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export function CoreStatus() {
  const [online, setOnline] = useState<boolean | null>(null);
  useEffect(() => { api.status().then(() => setOnline(true)).catch(() => setOnline(false)); }, []);
  const label = online === null ? "checking" : online ? "online" : "unreachable";
  const shape = online === null ? "unknown" : online ? "online" : "problem";
  return <div className="core-state" title={`Core service: ${label}`}><span className="status-dot" data-status={shape} /><span>Core {label}</span></div>;
}
