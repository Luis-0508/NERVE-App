package agentproto

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/nerve-labs/nerve/internal/redact"
)

func TestCommandResultRedactIsStable(t *testing.T) {
	result := CommandResult{Status: CommandFailed, FailureCode: "collection_failed",
		Output:        json.RawMessage(`{"commandLine":"tool --password=hunter2"}`),
		Stdout:        "Authorization: Bearer abcdefghijklmnop",
		Stderr:        "fetch https://ci:token123@example.com failed",
		FailureReason: "login with password=hunter2 failed"}
	result.Redact() // the agent, before upload
	first, _ := json.Marshal(result)
	want := redact.Counts{redact.KindPassword: 2, redact.KindBearerToken: 1, redact.KindURLCredentials: 1}
	if !maps.Equal(result.Redactions, want) {
		t.Fatalf("redactions = %v, want %v", result.Redactions, want)
	}
	result.Redact() // Core, on receipt
	second, _ := json.Marshal(result)
	if string(first) != string(second) {
		t.Fatalf("second pass changed the result:\n%s\n%s", first, second)
	}

	clean := CommandResult{Status: CommandSucceeded, Output: json.RawMessage(`{"cpu":3}`)}
	clean.Redact()
	if clean.Redactions != nil || string(clean.Output) != `{"cpu":3}` {
		t.Fatalf("clean result changed: %+v", clean)
	}
}
