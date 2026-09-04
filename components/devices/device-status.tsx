import type { DeviceStatus as DeviceStatusValue } from "@/lib/types";

const labels: Record<DeviceStatusValue, string> = {
  online: "Online",
  offline: "Offline",
  warning: "Warning",
  unknown: "Unknown",
  stopped: "Stopped",
  starting: "Starting",
};

export function DeviceStatus({ status }: { status: DeviceStatusValue }) {
  return <span className="device-status"><span className="status-dot" data-status={status} aria-hidden="true" />{labels[status]}</span>;
}
