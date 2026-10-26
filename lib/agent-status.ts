// Labels for the agent connection state reported by Core. "Stopped" means the
// agent announced a graceful shutdown; "Offline" means it went silent.
export type AgentConnection = "online" | "offline" | "stopped" | "never_seen" | "inactive";

export function agentConnectionState(status: string, connection: AgentConnection | undefined, online: boolean) {
  if (status === "removed") return { label: "Removed", dot: "stopped" };
  if (status === "revoked") return { label: "Revoked", dot: "warning" };
  if (status === "pending") return { label: online ? "Awaiting approval · online" : "Awaiting approval", dot: "warning" };
  switch (connection ?? (online ? "online" : "offline")) {
    case "online": return { label: "Online", dot: "online" };
    case "stopped": return { label: "Stopped", dot: "stopped" };
    case "never_seen": return { label: "Never connected", dot: "offline" };
    default: return { label: "Offline", dot: "offline" };
  }
}

const runModeLabels: Record<string, string> = { windows_service: "Windows service", systemd: "systemd service", foreground: "Foreground process" };

export function runModeLabel(runMode?: string) {
  return runMode ? runModeLabels[runMode] ?? runMode : undefined;
}
