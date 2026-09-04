import { SettingsView } from "@/components/settings/settings-view";
import { AppShell } from "@/components/shell/app-shell";

export default function SettingsPage() {
  return (
    <AppShell active="settings">
      <SettingsView />
    </AppShell>
  );
}

