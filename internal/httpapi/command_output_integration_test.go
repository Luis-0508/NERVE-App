//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/httpapi"
	"github.com/nerve-labs/nerve/internal/testsupport"
)

// Output of a privileged command is visible only to operators who may run
// privileged commands; the command itself stays visible with RemoteView.
func TestPrivilegedCommandOutputIsWithheld(t *testing.T) {
	pool := testsupport.MigratedPool(t, "http_output")
	adminID := testsupport.CreateUser(t, pool, "admin@example.invalid", "integration-password", "administrator")
	testsupport.CreateUser(t, pool, "tech@example.invalid", "integration-password", "technician")
	server := httptest.NewServer(httpapi.New(pool, config.Config{AllowedOrigin: "http://localhost:3000", SessionTTL: time.Hour, CoreSigningKey: testsupport.CoreSigningKeyHex},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test").Handler())
	defer server.Close()
	admin := login(t, server.URL, "admin@example.invalid", "integration-password")
	technician := login(t, server.URL, "tech@example.invalid", "integration-password")

	var created struct {
		Token string `json:"token"`
	}
	decodeResponse(t, doJSON(t, admin, http.MethodPost, server.URL+"/api/v1/agent-enrollments", `{}`, nil), &created)
	agent := enrollTestAgent(t, server.URL, created.Token, "managed-host")

	// No privileged command type is enabled yet, so the rows are written directly.
	ctx := context.Background()
	commandIDs := map[string]string{}
	for _, risk := range []string{"read_only", "privileged"} {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO remote_commands (agent_id, command_type, risk, requested_by, expires_at, timeout_seconds,
			max_output_bytes, status, claimed_at, claimed_by_credential, completed_at)
			VALUES ($1::uuid, 'test.output', $2, $3::uuid, now() + interval '5 minutes', 60, 4096, 'succeeded', now(), $4::uuid, now())
			RETURNING id::text`, agent.agentID, risk, adminID, agent.credentialID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO remote_command_results (command_id, stdout, output_bytes, result_digest)
			VALUES ($1::uuid, 'transcript text', 15, repeat('a', 64))`, id); err != nil {
			t.Fatal(err)
		}
		commandIDs[risk] = id
	}
	read := func(client *http.Client, id string) (stdout string, withheld bool) {
		t.Helper()
		var command struct {
			Result *struct {
				Stdout         string `json:"stdout"`
				OutputWithheld bool   `json:"outputWithheld"`
			} `json:"result"`
		}
		response := doJSON(t, client, http.MethodGet, server.URL+"/api/v1/remote/commands/"+id, "", nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("command detail = %d: %s", response.StatusCode, readBody(response))
		}
		decodeResponse(t, response, &command)
		if command.Result == nil {
			t.Fatalf("command %s has no result", id)
		}
		return command.Result.Stdout, command.Result.OutputWithheld
	}
	for _, test := range []struct {
		client   *http.Client
		risk     string
		stdout   string
		withheld bool
	}{
		{technician, "read_only", "transcript text", false},
		{technician, "privileged", "", true},
		{admin, "privileged", "transcript text", false},
	} {
		if stdout, withheld := read(test.client, commandIDs[test.risk]); stdout != test.stdout || withheld != test.withheld {
			t.Fatalf("%s output: stdout %q withheld %v, want %q %v", test.risk, stdout, withheld, test.stdout, test.withheld)
		}
	}
}
