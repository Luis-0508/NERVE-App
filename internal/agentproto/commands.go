package agentproto

import (
	"encoding/json"
	"time"
)

const (
	// MaxClaimWait bounds long polling so requests finish well inside proxy
	// read timeouts (Nginx is configured for 65 seconds).
	MaxClaimWait = 25 * time.Second
	// MaxResultRequestBytes bounds a result submission including JSON overhead.
	MaxResultRequestBytes = 2 << 20

	CommandSucceeded = "succeeded"
	CommandFailed    = "failed"
	CommandTimedOut  = "timed_out"

	CodeCommandNotFound         = "command_not_found"
	CodeCommandCancelled        = "command_cancelled"
	CodeCommandNotActive        = "command_not_active"
	CodeCommandAlreadyCompleted = "command_already_completed"
	CodeResultTooLarge          = "result_too_large"
)

type ClaimRequest struct {
	WaitSeconds int `json:"waitSeconds"`
}

// Command is delivered to exactly one agent exactly once.
type Command struct {
	ID             string         `json:"id"`
	AgentID        string         `json:"agentId"`
	Type           string         `json:"type"`
	Parameters     map[string]any `json:"parameters"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	MaxOutputBytes int            `json:"maxOutputBytes"`
	Deadline       time.Time      `json:"deadline"`
}

// CommandResult is submitted once per command. Resubmitting an identical
// result (for example after a lost response) is acknowledged idempotently.
type CommandResult struct {
	Status          string          `json:"status"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	Stdout          string          `json:"stdout,omitempty"`
	Stderr          string          `json:"stderr,omitempty"`
	OutputTruncated bool            `json:"outputTruncated,omitempty"`
	StdoutTruncated bool            `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool            `json:"stderrTruncated,omitempty"`
	FailureCode     string          `json:"failureCode,omitempty"`
	FailureReason   string          `json:"failureReason,omitempty"`
}

type ResultAck struct {
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate,omitempty"`
}
