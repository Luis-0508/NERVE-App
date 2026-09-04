import { Overview } from "@/components/overview/overview";
import { AppShell } from "@/components/shell/app-shell";

export default function OverviewPage() {
  return (
    <AppShell active="overview">
      <Overview />
    </AppShell>
  );
}

