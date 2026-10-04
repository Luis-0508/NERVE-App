package remote

import (
	"fmt"
	"math"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/authz"
)

// Policy describes how NERVE accepts one command type. Only types listed here
// can be queued; privileged and interactive capabilities have no policy yet,
// so they are refused regardless of what an agent advertises.
type Policy struct {
	Type           string
	Risk           agentproto.Risk
	Permission     authz.Permission
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int
	normalize      func(map[string]any) (map[string]any, error)
}

func noParameters(parameters map[string]any) (map[string]any, error) {
	if len(parameters) != 0 {
		return nil, fmt.Errorf("%w: this command takes no parameters", ErrInvalidParameters)
	}
	return map[string]any{}, nil
}

func processListParameters(parameters map[string]any) (map[string]any, error) {
	limit := 500
	for key, value := range parameters {
		if key != "limit" {
			return nil, fmt.Errorf("%w: unknown parameter %q", ErrInvalidParameters, key)
		}
		number, ok := value.(float64)
		if !ok || number != math.Trunc(number) || number < 1 || number > 1000 {
			return nil, fmt.Errorf("%w: limit must be an integer between 1 and 1000", ErrInvalidParameters)
		}
		limit = int(number)
	}
	return map[string]any{"limit": limit}, nil
}

var policies = map[string]Policy{
	agentproto.CapabilitySystemInfo: {Type: agentproto.CapabilitySystemInfo, Risk: agentproto.RiskReadOnly, Permission: authz.RemoteExecuteReadOnly,
		DefaultTimeout: 20 * time.Second, MaxTimeout: time.Minute, MaxOutputBytes: 16 << 10, normalize: noParameters},
	agentproto.CapabilitySystemMetrics: {Type: agentproto.CapabilitySystemMetrics, Risk: agentproto.RiskReadOnly, Permission: authz.RemoteExecuteReadOnly,
		DefaultTimeout: 20 * time.Second, MaxTimeout: time.Minute, MaxOutputBytes: 64 << 10, normalize: noParameters},
	agentproto.CapabilityProcessList: {Type: agentproto.CapabilityProcessList, Risk: agentproto.RiskReadOnly, Permission: authz.RemoteExecuteReadOnly,
		DefaultTimeout: 30 * time.Second, MaxTimeout: 2 * time.Minute, MaxOutputBytes: 512 << 10, normalize: processListParameters},
	agentproto.CapabilityServiceList: {Type: agentproto.CapabilityServiceList, Risk: agentproto.RiskReadOnly, Permission: authz.RemoteExecuteReadOnly,
		DefaultTimeout: 30 * time.Second, MaxTimeout: 2 * time.Minute, MaxOutputBytes: 512 << 10, normalize: noParameters},
	agentproto.CapabilitySoftwareList: {Type: agentproto.CapabilitySoftwareList, Risk: agentproto.RiskReadOnly, Permission: authz.RemoteExecuteReadOnly,
		DefaultTimeout: time.Minute, MaxTimeout: 2 * time.Minute, MaxOutputBytes: 1 << 20, normalize: noParameters},
}

// PolicyFor returns the policy for an enabled command type.
func PolicyFor(commandType string) (Policy, bool) {
	policy, ok := policies[commandType]
	return policy, ok
}

// Normalize validates parameters and returns the canonical stored form.
func (p Policy) Normalize(parameters map[string]any) (map[string]any, error) {
	if parameters == nil {
		parameters = map[string]any{}
	}
	return p.normalize(parameters)
}
