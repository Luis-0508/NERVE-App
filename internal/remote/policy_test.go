package remote

import (
	"errors"
	"testing"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/authz"
)

func TestBuiltInPoliciesKeepTheGateInvariants(t *testing.T) {
	for name, policy := range policies {
		info, known := agentproto.LookupCapability(name)
		if !known || info.Risk != policy.Risk || policy.Type != name {
			t.Errorf("%s: not a catalogued %s capability", name, policy.Risk)
		}
		if policy.Risk != agentproto.RiskReadOnly && (!policy.RequiresApproval || policy.Permission != authz.RemoteExecutePrivileged) {
			t.Errorf("%s: a %s command must require approval and %s", name, policy.Risk, authz.RemoteExecutePrivileged)
		}
		if policy.Risk == agentproto.RiskInteractive {
			t.Errorf("%s: interactive sessions belong to MeshCentral (decision 0006)", name)
		}
	}
	for _, name := range []string{agentproto.CapabilityCommandShell, agentproto.CapabilityCommandPowerShell, agentproto.CapabilityScriptRun} {
		if _, enabled := PolicyFor(name); enabled {
			t.Errorf("%s must stay disabled (decision 0006)", name)
		}
	}
}

func TestManagementParameters(t *testing.T) {
	valid := []struct {
		commandType string
		parameters  map[string]any
	}{
		{agentproto.CapabilityServiceRestart, map[string]any{"name": "nginx.service"}},
		{agentproto.CapabilityServiceStop, map[string]any{"name": "Spooler"}},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 4242.0, "name": "notepad.exe"}},
		{agentproto.CapabilitySystemReboot, map[string]any{}},
		{agentproto.CapabilitySystemShutdown, nil},
		{agentproto.CapabilityLogsRead, nil},
		{agentproto.CapabilityLogsRead, map[string]any{"source": "Service Control Manager", "lines": 500.0, "sinceMinutes": 10080.0}},
		{agentproto.CapabilityLogsRead, map[string]any{"source": "nginx.service"}},
	}
	for _, tc := range valid {
		policy, _ := PolicyFor(tc.commandType)
		if _, err := policy.Normalize(tc.parameters); err != nil {
			t.Errorf("%s %v: %v", tc.commandType, tc.parameters, err)
		}
	}
	invalid := []struct {
		commandType string
		parameters  map[string]any
	}{
		{agentproto.CapabilityServiceRestart, map[string]any{}},
		{agentproto.CapabilityServiceRestart, map[string]any{"name": "--force"}},
		{agentproto.CapabilityServiceRestart, map[string]any{"name": "a b"}},
		{agentproto.CapabilityServiceRestart, map[string]any{"name": "../etc/passwd"}},
		{agentproto.CapabilityServiceStop, map[string]any{"name": "nginx", "extra": true}},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 1.0, "name": "init"}},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 12.5, "name": "x"}},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 42.0}},
		{agentproto.CapabilityProcessTerminate, map[string]any{"pid": 42.0, "name": "/usr/bin/x"}},
		{agentproto.CapabilitySystemReboot, map[string]any{"delaySeconds": 5.0}},
		{agentproto.CapabilityLogsRead, map[string]any{"lines": 501.0}},
		{agentproto.CapabilityLogsRead, map[string]any{"sinceMinutes": 0.0}},
		{agentproto.CapabilityLogsRead, map[string]any{"source": "x' or '1'='1"}},
		{agentproto.CapabilityLogsRead, map[string]any{"source": " leading"}},
		{agentproto.CapabilityLogsRead, map[string]any{"source": "Security", "log": "Security"}},
	}
	for _, tc := range invalid {
		policy, _ := PolicyFor(tc.commandType)
		if _, err := policy.Normalize(tc.parameters); !errors.Is(err, ErrInvalidParameters) {
			t.Errorf("%s %v: err = %v, want ErrInvalidParameters", tc.commandType, tc.parameters, err)
		}
	}
}
