package main

import (
	"context"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// runtimeReporter describes this process in heartbeats: how it runs, when it
// started and when the host booted (so operators can tell a reboot from a
// service restart).
func runtimeReporter(runMode string, startedAt time.Time) func(stopping bool) *agentproto.AgentRuntime {
	return func(stopping bool) *agentproto.AgentRuntime {
		value := &agentproto.AgentRuntime{RunMode: runMode, StartedAt: startedAt.Unix(), Stopping: stopping}
		if uptime := systemUptime(); uptime > 0 {
			value.HostBootAt = time.Now().Add(-uptime).Unix()
		}
		return value
	}
}

// reportStopping sends a final heartbeat so Core shows the agent as stopped
// right away. It is best effort and bounded: a host that is shutting down must
// not wait for an unreachable Core.
func (m *manager) reportStopping() {
	if m.runtime == nil || m.legacyCore.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := agentproto.HeartbeatRequest{Host: m.facts(), Capabilities: m.capabilities(), LastError: m.currentError(), Runtime: m.runtime(true)}
	if !m.legacyTransport.Load() {
		request.Transport = m.client.transport()
	}
	var response agentproto.HeartbeatResponse
	if _, err := m.client.post(ctx, "/agent/heartbeat", request, &response); err != nil {
		m.logger.Warn("final heartbeat not delivered", "error", err)
		return
	}
	m.logger.Info("reported shutdown to Core")
}
