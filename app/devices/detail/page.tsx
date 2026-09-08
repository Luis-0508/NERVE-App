import { DeviceDetailView } from "@/components/devices/device-detail-view";
import { AppShell } from "@/components/shell/app-shell";

export default function DeviceDetailPage() {
  return <AppShell active="devices"><DeviceDetailView /></AppShell>;
}
