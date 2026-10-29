package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// run starts all management loops and returns when ctx ends.
func (m *manager) run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		m.runHeartbeats(ctx)
	}()
	go func() {
		defer workers.Done()
		m.runCommands(ctx)
	}()
	workers.Wait()
}

// runCommands long-polls Core for work and executes one command at a time.
// Core guarantees each command is handed out once; the agent additionally
// remembers recent IDs so a misbehaving server cannot make it run one twice.
func (m *manager) runCommands(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		var command agentproto.Command
		status, err := m.client.post(ctx, "/agent/commands/claim", agentproto.ClaimRequest{WaitSeconds: int(agentproto.MaxClaimWait.Seconds())}, &command)
		switch {
		case ctx.Err() != nil:
			return
		case requiresReenrollment(err):
			m.logger.Error("command polling stopped: credential rejected; re-enroll this agent", "error", err)
			if !sleepContext(ctx, 10*time.Minute) {
				return
			}
			continue
		case err != nil:
			failures++
			wait := backoff(failures)
			m.setError("command claim: " + err.Error())
			m.logger.Warn("command claim failed", "error", err, "retry_in", wait.String())
			if !sleepContext(ctx, wait) {
				return
			}
			continue
		}
		failures = 0
		if status == http.StatusNoContent || command.ID == "" {
			continue
		}
		m.execute(ctx, command)
	}
}

func (m *manager) markExecuted(commandID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executed == nil {
		m.executed = map[string]time.Time{}
	}
	if _, seen := m.executed[commandID]; seen {
		return false
	}
	if len(m.executed) >= 1024 {
		for id, at := range m.executed {
			if time.Since(at) > time.Hour {
				delete(m.executed, id)
			}
		}
	}
	m.executed[commandID] = time.Now()
	return true
}

func (m *manager) execute(ctx context.Context, command agentproto.Command) {
	logger := m.logger.With("command_id", command.ID, "type", command.Type)
	if command.AgentID != m.agentID {
		logger.Error("refusing a command addressed to another agent", "target_agent", command.AgentID)
		return
	}
	if !m.markExecuted(command.ID) {
		logger.Error("refusing to execute a command twice")
		return
	}
	if time.Now().After(command.Deadline) {
		m.submit(ctx, command, failed(agentproto.CommandTimedOut, "deadline_passed", "The command deadline passed before execution started."))
		return
	}
	// Anything beyond read-only needs HTTPS and Core's signed approval for
	// exactly this command, checked here independently of Core's own gate.
	if info, catalogued := agentproto.LookupCapability(command.Type); !catalogued || info.Risk != agentproto.RiskReadOnly {
		if m.client.transport() != agentproto.TransportHTTPS {
			logger.Error("refusing a privileged command over plain HTTP")
			m.submit(ctx, command, failed(agentproto.CommandFailed, "insecure_transport", "Privileged commands need an HTTPS connection to Core."))
			return
		}
		if err := agentproto.VerifyCommandApproval(m.client.coreKey, command, time.Now()); err != nil {
			logger.Error("refusing a privileged command without a valid approval", "error", err)
			m.submit(ctx, command, failed(agentproto.CommandFailed, "approval_rejected", err.Error()))
			return
		}
	}
	collect, known := m.collectorFor(command.Type)
	if !known || !m.supported(command.Type) {
		m.submit(ctx, command, failed(agentproto.CommandFailed, "unsupported_capability", "This agent does not support "+command.Type+"."))
		return
	}
	if _, err := m.client.post(ctx, "/agent/commands/"+command.ID+"/start", struct{}{}, nil); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusConflict || apiErr.Status == http.StatusNotFound) {
			logger.Info("command no longer runnable", "reason", apiErr.Code)
			return
		}
		logger.Warn("command start could not be confirmed; not executing", "error", err)
		return
	}

	started := time.Now()
	deadline := command.Deadline
	if limit := started.Add(time.Duration(command.TimeoutSeconds) * time.Second); limit.Before(deadline) {
		deadline = limit
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	output, err := collect(runCtx, command.Parameters)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	cancel()

	var result agentproto.CommandResult
	switch {
	case ctx.Err() != nil:
		// The agent is stopping. Report the interruption instead of leaving
		// the command to Core's timeout sweeper.
		result = failed(agentproto.CommandFailed, "agent_stopped", "The agent stopped before the command finished.")
		submitCtx, cancelSubmit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelSubmit()
		logger.Info("command interrupted by agent shutdown")
		m.submit(submitCtx, command, result)
		return
	case timedOut:
		result = failed(agentproto.CommandTimedOut, "timeout", "The command exceeded its timeout.")
	case err != nil:
		result = failed(agentproto.CommandFailed, "collection_failed", err.Error())
		m.setError(command.Type + ": " + err.Error())
	default:
		encoded, truncated, encodeErr := encodeOutput(output, command.MaxOutputBytes)
		if encodeErr != nil {
			result = failed(agentproto.CommandFailed, "output_too_large", encodeErr.Error())
		} else {
			result = agentproto.CommandResult{Status: agentproto.CommandSucceeded, Output: encoded, OutputTruncated: truncated}
		}
	}
	logger.Info("command finished", "status", result.Status, "duration_ms", time.Since(started).Milliseconds())
	m.submit(ctx, command, result)
}

func failed(status, code, reason string) agentproto.CommandResult {
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	return agentproto.CommandResult{Status: status, FailureCode: code, FailureReason: reason}
}

// submit delivers a result, retrying transient failures. Core acknowledges an
// identical resubmission, so a retry after a lost response is safe.
func (m *manager) submit(ctx context.Context, command agentproto.Command, result agentproto.CommandResult) {
	// Secrets never leave the host; Core applies the same rules again.
	result.Redact()
	giveUp := time.Now().Add(5 * time.Minute)
	for attempt := 1; ; attempt++ {
		var ack agentproto.ResultAck
		_, err := m.client.post(ctx, "/agent/commands/"+command.ID+"/result", result, &ack)
		if err == nil {
			return
		}
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusUnauthorized {
			if apiErr.Status == http.StatusRequestEntityTooLarge && result.Status == agentproto.CommandSucceeded {
				result = failed(agentproto.CommandFailed, "output_too_large", "The result exceeded the server limit.")
				continue
			}
			m.logger.Warn("command result rejected", "command_id", command.ID, "code", apiErr.Code)
			return
		}
		if ctx.Err() != nil || time.Now().After(giveUp) {
			m.logger.Error("command result could not be delivered", "command_id", command.ID, "error", err)
			return
		}
		if !sleepContext(ctx, backoff(attempt)) {
			return
		}
	}
}

func (m *manager) collectorFor(name string) (collector, bool) {
	if m.collectors != nil {
		item, ok := m.collectors[name]
		return item, ok
	}
	return collectorFor(name)
}

func (m *manager) supported(name string) bool {
	for _, item := range m.capabilities() {
		if item == name {
			return true
		}
	}
	return false
}
