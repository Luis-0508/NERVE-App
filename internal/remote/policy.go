package remote

import (
	"errors"
	"fmt"
	"math"
	"sync"
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
	// RequiresApproval sends the command through the approval gate: HTTPS
	// transport, a trusted device, a matching remote scope and an operator's
	// approval before any agent can claim it. Every non-read-only policy
	// requires it; RegisterPolicy enforces that.
	RequiresApproval bool
	// ForbidSelfApproval requires a second operator. By default the requester
	// may approve their own command (maintainer decision, 2026-10-22).
	ForbidSelfApproval bool
	normalize          func(map[string]any) (map[string]any, error)
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

var policiesMu sync.RWMutex

// PolicyFor returns the policy for an enabled command type.
func PolicyFor(commandType string) (Policy, bool) {
	policiesMu.RLock()
	defer policiesMu.RUnlock()
	policy, ok := policies[commandType]
	return policy, ok
}

// RegisterPolicy enables an additional command type and returns a function
// that disables it again. NERVE registers no privileged type yet; the
// privileged helper's fixed allowlist (#91) will, and tests use it to exercise
// the approval gate. The type must be in the capability catalogue with the
// same risk, and anything beyond read-only must require approval. A policy
// without a parameter validator accepts no parameters.
func RegisterPolicy(policy Policy) (func(), error) {
	info, known := agentproto.LookupCapability(policy.Type)
	switch {
	case !known || info.Risk != policy.Risk:
		return nil, fmt.Errorf("%w: %q is not a catalogued %s capability", ErrUnknownCommand, policy.Type, policy.Risk)
	case policy.Risk != agentproto.RiskReadOnly && (!policy.RequiresApproval || policy.Permission != authz.RemoteExecutePrivileged):
		return nil, fmt.Errorf("%s commands must require approval and the %s permission", policy.Risk, authz.RemoteExecutePrivileged)
	case policy.DefaultTimeout <= 0 || policy.MaxTimeout < policy.DefaultTimeout || policy.MaxOutputBytes < 1024 || policy.MaxOutputBytes > 1<<20:
		return nil, errors.New("policy timeouts or output limit are out of range")
	}
	policiesMu.Lock()
	defer policiesMu.Unlock()
	if _, exists := policies[policy.Type]; exists {
		return nil, fmt.Errorf("a policy for %q is already registered", policy.Type)
	}
	policies[policy.Type] = policy
	return func() {
		policiesMu.Lock()
		delete(policies, policy.Type)
		policiesMu.Unlock()
	}, nil
}

// Normalize validates parameters and returns the canonical stored form.
func (p Policy) Normalize(parameters map[string]any) (map[string]any, error) {
	if parameters == nil {
		parameters = map[string]any{}
	}
	if p.normalize == nil {
		return noParameters(parameters)
	}
	return p.normalize(parameters)
}
