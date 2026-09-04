import { DevicesView } from "@/components/devices/devices-view";
import { AppShell } from "@/components/shell/app-shell";

export default function DevicesPage() {
  return <AppShell active="devices"><DevicesView /></AppShell>;
}

