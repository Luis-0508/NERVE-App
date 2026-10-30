package agentproto

import (
	"encoding/json"
	"time"

	"github.com/nerve-labs/nerve/internal/redact"
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
	// Approval is set for privileged commands; see VerifyCommandApproval.
	Approval *CommandApproval `json:"approval,omitempty"`
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
	// Redactions counts the secrets replaced per kind (see internal/redact).
	Redactions redact.Counts `json:"redactions,omitempty"`
}

// Redact replaces secrets in the output, transcripts and failure reason and
// adds what it replaced to Redactions. The agent calls it before upload and
// Core again on receipt; a second pass finds nothing new.
func (r *CommandResult) Redact() {
	found := redact.Counts{}
	add := func(text string) string {
		redacted, counts := redact.Text(text)
		found.Add(counts)
		return redacted
	}
	r.Stdout, r.Stderr, r.FailureReason = add(r.Stdout), add(r.Stderr), add(r.FailureReason)
	output, counts := redact.JSON(r.Output)
	r.Output = output
	found.Add(counts)
	if len(found) == 0 {
		return
	}
	if r.Redactions == nil {
		r.Redactions = redact.Counts{}
	}
	r.Redactions.Add(found)
}

type ResultAck struct {
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate,omitempty"`
}
