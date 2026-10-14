package agentproto

import (
	"errors"
	"time"
)

// Capability names are a stable contract between agents, Core and any future
// remote-management provider (for example MeshCentral). A provider advertises a
// capability only when it can really perform it.
const (
	CapabilitySystemInfo    = "system.info"
	CapabilitySystemMetrics = "system.metrics"
	CapabilityProcessList   = "process.list"
	CapabilityServiceList   = "service.list"
	CapabilitySoftwareList  = "software.list"

	// Reserved for later milestones. Core refuses to queue them today.
	CapabilityCommandShell      = "command.shell"
	CapabilityCommandPowerShell = "command.powershell"
	CapabilityScriptRun         = "script.run"
	CapabilitySystemReboot      = "system.reboot"
	CapabilityFileTransfer      = "file.transfer"
	CapabilityRemoteDesktop     = "remote.desktop"
	CapabilityRemoteTerminal    = "remote.terminal"
)

// Risk classifies what a capability can do to an endpoint.
type Risk string

const (
	RiskReadOnly    Risk = "read_only"
	RiskPrivileged  Risk = "privileged"
	RiskInteractive Risk = "interactive"
)

type CapabilityInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Risk        Risk   `json:"risk"`
	Description string `json:"description"`
}

// Catalogue lists every capability NERVE knows. Agents may report only these;
// unknown names are discarded by Core.
var Catalogue = []CapabilityInfo{
	{CapabilitySystemInfo, "System info", RiskReadOnly, "Hostname, operating system, architecture, uptime and agent version"},
	{CapabilitySystemMetrics, "Metrics", RiskReadOnly, "Current CPU, memory and disk usage"},
	{CapabilityProcessList, "Processes", RiskReadOnly, "Bounded list of running processes, without command lines"},
	{CapabilityServiceList, "Services", RiskReadOnly, "Installed services and their state"},
	{CapabilitySoftwareList, "Software", RiskReadOnly, "Installed software packages"},
	{CapabilityCommandShell, "Shell command", RiskPrivileged, "Run a shell command (not enabled)"},
	{CapabilityCommandPowerShell, "PowerShell command", RiskPrivileged, "Run a PowerShell command (not enabled)"},
	{CapabilityScriptRun, "Run script", RiskPrivileged, "Run a catalogued script (not enabled)"},
	{CapabilitySystemReboot, "Reboot", RiskPrivileged, "Restart the endpoint (not enabled)"},
	{CapabilityFileTransfer, "File transfer", RiskInteractive, "Transfer files (not enabled)"},
	{CapabilityRemoteDesktop, "Remote desktop", RiskInteractive, "Interactive desktop session (not enabled)"},
	{CapabilityRemoteTerminal, "Terminal", RiskInteractive, "Interactive terminal session (not enabled)"},
}

// LookupCapability returns catalogue metadata for name.
func LookupCapability(name string) (CapabilityInfo, bool) {
	for _, info := range Catalogue {
		if info.Name == name {
			return info, true
		}
	}
	return CapabilityInfo{}, false
}

// FilterCapabilities keeps known, unique capability names in catalogue order.
func FilterCapabilities(names []string) []string {
	reported := make(map[string]bool, len(names))
	for _, name := range names {
		reported[name] = true
	}
	result := make([]string, 0, len(reported))
	for _, info := range Catalogue {
		if reported[info.Name] {
			result = append(result, info.Name)
		}
	}
	return result
}

// HeartbeatRequest reports liveness, host facts and supported capabilities.
type HeartbeatRequest struct {
	Host         HostFacts     `json:"host"`
	Capabilities []string      `json:"capabilities"`
	LastError    string        `json:"lastError,omitempty"`
	Runtime      *AgentRuntime `json:"runtime,omitempty"`
}

// Run modes reported by the agent.
const (
	RunModeWindowsService = "windows_service"
	RunModeSystemd        = "systemd"
	RunModeForeground     = "foreground"
)

// AgentRuntime describes the agent process. Agents before this field omit it.
type AgentRuntime struct {
	RunMode string `json:"runMode"`
	// StartedAt and HostBootAt are Unix seconds.
	StartedAt  int64 `json:"startedAt"`
	HostBootAt int64 `json:"hostBootAt,omitempty"`
	// Stopping marks the final heartbeat of a graceful shutdown, so Core can
	// show the agent as stopped instead of waiting for the online window.
	Stopping bool `json:"stopping,omitempty"`
}

// Validate rejects unknown run modes and implausible timestamps.
func (r AgentRuntime) Validate(now time.Time) error {
	switch r.RunMode {
	case RunModeWindowsService, RunModeSystemd, RunModeForeground:
	default:
		return errors.New("runtime has an unknown run mode")
	}
	latest := now.Add(MaxClockSkew).Unix()
	if r.StartedAt <= 0 || r.StartedAt > latest || r.HostBootAt < 0 || r.HostBootAt > latest {
		return errors.New("runtime timestamps are out of range")
	}
	return nil
}

type HeartbeatResponse struct {
	ServerTime               int64 `json:"serverTime"`
	HeartbeatIntervalSeconds int   `json:"heartbeatIntervalSeconds"`
}
