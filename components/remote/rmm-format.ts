import type { RemoteCommandStatus } from "@/lib/types";

export const activeStatuses: RemoteCommandStatus[] = ["awaiting_approval", "queued", "claimed", "running"];

export const statusLabels: Record<RemoteCommandStatus, string> = {
  awaiting_approval: "Awaiting approval", queued: "Queued", claimed: "Claimed", running: "Running", succeeded: "Succeeded",
  failed: "Failed", timed_out: "Timed out", cancelled: "Cancelled", expired: "Expired",
};

// Status shapes for command states (DESIGN.md).
export const commandStatusDots: Record<RemoteCommandStatus, string> = {
  awaiting_approval: "warning", queued: "info", claimed: "info", running: "info", succeeded: "online",
  failed: "problem", timed_out: "problem", cancelled: "stopped", expired: "stopped",
};

export function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(new Date(value));
}

export function formatBytes(value: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let size = value, unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${size >= 100 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

export function formatDuration(seconds: number) {
  const days = Math.floor(seconds / 86400), hours = Math.floor((seconds % 86400) / 3600), minutes = Math.floor((seconds % 3600) / 60);
  if (days) return `${days} d ${hours} h`;
  if (hours) return `${hours} h ${minutes} min`;
  return `${minutes} min`;
}
