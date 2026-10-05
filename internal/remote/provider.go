// Package remote is the provider-neutral remote-management domain. The UI and
// API ask it what a device can do and request bounded commands without knowing
// whether the native NERVE agent, MeshCentral or another adapter does the work.
package remote

import (
	"context"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
)

// ProviderNativeAgent is the NERVE management agent. A MeshCentral adapter
// would register as "meshcentral" and map its node IDs to NERVE devices through
// operator-confirmed device identities.
const ProviderNativeAgent = "nerve-agent"

// Capability is a provider-reported capability with NERVE catalogue metadata.
type Capability struct {
	agentproto.CapabilityInfo
	// Executable reports whether NERVE will accept a command for it today.
	Executable bool `json:"executable"`
}

// Endpoint is one way to manage a device remotely.
type Endpoint struct {
	Provider     string       `json:"provider"`
	EndpointID   string       `json:"endpointId"`
	DisplayName  string       `json:"displayName"`
	Status       string       `json:"status"`
	Online       bool         `json:"online"`
	Version      string       `json:"version,omitempty"`
	LastSeenAt   *time.Time   `json:"lastSeenAt,omitempty"`
	Association  string       `json:"association"`
	Capabilities []Capability `json:"capabilities"`
}

// Provider supplies remote-management endpoints. Interactive sessions
// (desktop, terminal, files) will be added as a separate optional interface so
// providers that cannot offer them do not need stub implementations.
type Provider interface {
	Name() string
	EndpointsForDevice(ctx context.Context, deviceID string) ([]Endpoint, error)
}

type Registry struct {
	providers []Provider
}

func NewRegistry(providers ...Provider) *Registry {
	return &Registry{providers: providers}
}

// DeviceEndpoints aggregates endpoints from every registered provider.
func (r *Registry) DeviceEndpoints(ctx context.Context, deviceID string) ([]Endpoint, error) {
	endpoints := make([]Endpoint, 0)
	for _, provider := range r.providers {
		items, err := provider.EndpointsForDevice(ctx, deviceID)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, items...)
	}
	return endpoints, nil
}

// NativeProvider exposes enrolled NERVE agents associated with a device.
type NativeProvider struct {
	Agents *agents.Repository
}

func (p NativeProvider) Name() string { return ProviderNativeAgent }

func (p NativeProvider) EndpointsForDevice(ctx context.Context, deviceID string) ([]Endpoint, error) {
	items, err := p.Agents.List(ctx, agents.ListFilter{DeviceID: deviceID})
	if err != nil {
		return nil, err
	}
	endpoints := make([]Endpoint, 0, len(items))
	for _, agent := range items {
		endpoints = append(endpoints, Endpoint{
			Provider: ProviderNativeAgent, EndpointID: agent.ID, DisplayName: agent.DisplayName, Status: agent.Status,
			Online: agent.Online, Version: agent.AgentVersion, LastSeenAt: agent.LastSeenAt, Association: agent.Association.Status,
			Capabilities: Capabilities(agent.Capabilities),
		})
	}
	return endpoints, nil
}

// Capabilities annotates reported names with catalogue metadata.
func Capabilities(names []string) []Capability {
	result := make([]Capability, 0, len(names))
	for _, name := range agentproto.FilterCapabilities(names) {
		info, _ := agentproto.LookupCapability(name)
		result = append(result, Capability{CapabilityInfo: info, Executable: Executable(name)})
	}
	return result
}

// Executable reports whether the command pipeline accepts a capability.
// Privileged and interactive capabilities stay disabled until their security
// prerequisites (confirmation, signed commands, policies) exist.
func Executable(name string) bool {
	_, ok := PolicyFor(name)
	return ok
}
