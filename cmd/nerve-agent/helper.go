package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// The privileged helper (decision 0007) runs approved privileged commands for
// the unprivileged agent over a local channel. It trusts only helper.json,
// which the installer writes as root from the enrolled identity, never files
// the agent account can change.

const (
	helperPinFileName    = "helper.json"
	helperReplayFileName = "executed.json"
	// helperMaxRequest bounds one request; a command with parameters and its
	// approval is a few kilobytes.
	helperMaxRequest = 1 << 20
	// helperMaxResponse bounds one response; output is capped by the command's
	// MaxOutputBytes later, but a helper must never make the agent buffer
	// without limit.
	helperMaxResponse = 8 << 20
	// helperReplayCapacity bounds the replay register. Approvals are made by
	// people, so it is never reached in normal use; when it is, the helper
	// refuses instead of forgetting IDs that could still be replayed.
	helperReplayCapacity = 4096
	// helperHealthTTL is how long the agent trusts one health answer.
	helperHealthTTL = 30 * time.Second
)

// helperHandlers is the compiled allowlist of privileged command types: the
// system-management commands (#92). Tests use their own handlers.
var helperHandlers = systemCommandHandlers()

type helperRequest struct {
	Op      string              `json:"op"` // "health" or "run"
	Command *agentproto.Command `json:"command,omitempty"`
}

type helperResponse struct {
	Code   string         `json:"code,omitempty"` // empty on success
	Reason string         `json:"reason,omitempty"`
	Output map[string]any `json:"output,omitempty"`
	Health *helperHealth  `json:"health,omitempty"`
}

type helperHealth struct {
	Version string   `json:"version"`
	AgentID string   `json:"agentId"`
	Types   []string `json:"types"`
}

// helperError is a refusal or failure reported by the helper. Code becomes the
// command's failure code.
type helperError struct {
	Code, Reason string
}

func (e *helperError) Error() string { return e.Code + ": " + e.Reason }

// helperPin is helper.json: the agent this helper serves and the Core keys
// whose approvals it accepts.
type helperPin struct {
	Version  int      `json:"version"`
	AgentID  string   `json:"agentId"`
	CoreKeys []string `json:"coreKeys"`

	keys []ed25519.PublicKey
}

func newHelperPin(id *identity) helperPin {
	return helperPin{Version: 1, AgentID: id.AgentID, CoreKeys: []string{base64.StdEncoding.EncodeToString(id.coreKey)}}
}

func parseHelperPin(raw []byte) (helperPin, error) {
	var pin helperPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		return helperPin{}, fmt.Errorf("parse %s: %w", helperPinFileName, err)
	}
	if pin.Version != 1 || pin.AgentID == "" || len(pin.CoreKeys) == 0 {
		return helperPin{}, fmt.Errorf("%s is incomplete; reinstall with `nerve-agent service install --with-helper`", helperPinFileName)
	}
	for _, value := range pin.CoreKeys {
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return helperPin{}, fmt.Errorf("%s holds an invalid Core key", helperPinFileName)
		}
		pin.keys = append(pin.keys, key)
	}
	return pin, nil
}

// verify checks the approval against every pinned key and the binding to the
// pinned agent.
func (p helperPin) verify(command agentproto.Command, now time.Time) error {
	if command.AgentID != p.AgentID {
		return &helperError{"agent_mismatch", "The command is addressed to another agent than this helper serves."}
	}
	err := agentproto.ErrApprovalInvalid
	for _, key := range p.keys {
		if err = agentproto.VerifyCommandApproval(key, command, now); !errors.Is(err, agentproto.ErrApprovalInvalid) {
			break
		}
	}
	if err != nil {
		return &helperError{"approval_rejected", err.Error()}
	}
	return nil
}

// replayStore remembers executed command IDs on disk until their approval can
// no longer be valid, so a restart of the helper does not reopen a replay.
type replayStore struct {
	path    string
	mu      sync.Mutex
	entries map[string]time.Time // command ID -> approval expiry
}

func openReplayStore(path string) (*replayStore, error) {
	store := &replayStore{path: path, entries: map[string]time.Time{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return store, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &store.entries); err != nil {
		// Refuse to start rather than forget what already ran.
		return nil, fmt.Errorf("replay register %s is corrupt: %w", path, err)
	}
	return store, nil
}

// claim records id before the command runs. It fails for an ID already seen
// and when the record cannot be made durable.
func (r *replayStore) claim(id string, expires, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.entries[id]; seen {
		return &helperError{"replayed", "This command was already executed."}
	}
	for key, at := range r.entries {
		// Keep entries one maximum approval lifetime beyond expiry, so a clock
		// stepped backwards cannot revive an approval whose entry is gone.
		if now.After(at.Add(agentproto.MaxApprovalTTL)) {
			delete(r.entries, key)
		}
	}
	if len(r.entries) >= helperReplayCapacity {
		return &helperError{"helper_busy", "The replay register is full; try again later."}
	}
	r.entries[id] = expires
	if err := r.persist(); err != nil {
		delete(r.entries, id)
		return &helperError{"helper_failed", "The replay register could not be written."}
	}
	return nil
}

func (r *replayStore) persist() error {
	encoded, err := json.Marshal(r.entries)
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.path)
	temporary, err := os.CreateTemp(dir, "."+helperReplayFileName+"-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, r.path); err != nil {
		return err
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		handle.Close()
	}
	return nil
}

// helperServer answers one request per connection. Commands run one at a time.
type helperServer struct {
	pin      helperPin
	replay   *replayStore
	handlers map[string]collector
	logger   *slog.Logger
	now      func() time.Time
	// authorized checks the connecting process (SO_PEERCRED on Linux).
	authorized func(net.Conn) error

	run sync.Mutex
}

func (s *helperServer) serve(ctx context.Context, listener net.Listener) error {
	var connections sync.WaitGroup
	defer connections.Wait()
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		connections.Go(func() { s.handleConn(ctx, connection) })
	}
}

func (s *helperServer) handleConn(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	if err := s.authorized(connection); err != nil {
		s.logger.Warn("refused a helper connection from an unauthorized process", "error", err)
		return
	}
	_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
	var request helperRequest
	if err := json.NewDecoder(io.LimitReader(connection, helperMaxRequest)).Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(helperResponse{Code: "bad_request", Reason: "The request could not be read."})
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	response := s.handle(ctx, request)
	_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = json.NewEncoder(connection).Encode(response)
}

func (s *helperServer) types() []string {
	names := make([]string, 0, len(s.handlers))
	for name := range s.handlers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *helperServer) handle(ctx context.Context, request helperRequest) helperResponse {
	switch {
	case request.Op == "health":
		return helperResponse{Health: &helperHealth{Version: version, AgentID: s.pin.AgentID, Types: s.types()}}
	case request.Op != "run" || request.Command == nil:
		return helperResponse{Code: "bad_request", Reason: "Unknown helper operation."}
	}
	command := *request.Command
	logger := s.logger.With("command_id", command.ID, "type", command.Type)
	refuse := func(err error) helperResponse {
		var refusal *helperError
		if !errors.As(err, &refusal) {
			refusal = &helperError{"helper_failed", err.Error()}
		}
		logger.Warn("refused a privileged command", "code", refusal.Code, "reason", refusal.Reason)
		return helperResponse{Code: refusal.Code, Reason: refusal.Reason}
	}
	now := s.now()
	if err := s.pin.verify(command, now); err != nil {
		return refuse(err)
	}
	handler, allowed := s.handlers[command.Type]
	if !allowed {
		return refuse(&helperError{"not_allowlisted", "This helper does not run " + command.Type + "."})
	}
	if !now.Before(command.Deadline) {
		return refuse(&helperError{"deadline_passed", "The command deadline passed before execution started."})
	}
	s.run.Lock()
	defer s.run.Unlock()
	if err := s.replay.claim(command.ID, command.Approval.ExpiresAt, now); err != nil {
		return refuse(err)
	}
	deadline := command.Deadline
	if limit := now.Add(time.Duration(command.TimeoutSeconds) * time.Second); limit.Before(deadline) {
		deadline = limit
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	logger.Info("running an approved privileged command", "approved_by", command.Approval.ApprovedBy)
	output, err := handler(runCtx, command.Parameters)
	if err != nil {
		logger.Warn("privileged command failed", "error", err)
		var failure *helperError
		if errors.As(err, &failure) {
			return helperResponse{Code: failure.Code, Reason: failure.Reason}
		}
		return helperResponse{Code: "handler_failed", Reason: err.Error()}
	}
	logger.Info("privileged command finished")
	return helperResponse{Output: output}
}

// privilegedRunner is the agent's view of the helper.
type privilegedRunner interface {
	// types lists the privileged command types the helper runs now; empty when
	// it is unreachable or serves another agent.
	types() []string
	run(ctx context.Context, command agentproto.Command) (map[string]any, error)
}

// helperClient talks to the helper. dial must authenticate the helper (on
// Linux: the socket is served by root).
type helperClient struct {
	dial    func(ctx context.Context) (net.Conn, error)
	agentID string
	logger  *slog.Logger

	mu        sync.Mutex
	checkedAt time.Time
	available []string
	mismatch  bool
}

func (c *helperClient) exchange(ctx context.Context, request helperRequest) (helperResponse, error) {
	connection, err := c.dial(ctx)
	if err != nil {
		return helperResponse{}, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return helperResponse{}, err
	}
	var response helperResponse
	if err := json.NewDecoder(io.LimitReader(connection, helperMaxResponse)).Decode(&response); err != nil {
		return helperResponse{}, err
	}
	return response, nil
}

func (c *helperClient) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.checkedAt.IsZero() && time.Since(c.checkedAt) < helperHealthTTL {
		return c.available
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.checkedAt, c.available = time.Now(), nil
	response, err := c.exchange(ctx, helperRequest{Op: "health"})
	if err != nil || response.Health == nil {
		return nil
	}
	if response.Health.AgentID != c.agentID {
		if !c.mismatch && c.logger != nil {
			c.logger.Warn("the privileged helper serves another agent identity; run `nerve-agent service install --with-helper` again after re-enrollment")
		}
		c.mismatch = true
		return nil
	}
	c.mismatch = false
	for _, name := range response.Health.Types {
		if info, ok := agentproto.LookupCapability(name); ok && info.Risk == agentproto.RiskPrivileged {
			c.available = append(c.available, name)
		}
	}
	return c.available
}

func (c *helperClient) run(ctx context.Context, command agentproto.Command) (map[string]any, error) {
	response, err := c.exchange(ctx, helperRequest{Op: "run", Command: &command})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &helperError{"helper_unavailable", "The privileged helper did not answer."}
	}
	if response.Code != "" {
		return nil, &helperError{response.Code, response.Reason}
	}
	return response.Output, nil
}

// withPrivileged adds the helper's command types to the agent's capabilities,
// except types disabled in config.json.
func withPrivileged(base func() []string, helper privilegedRunner, disabled []string) func() []string {
	return func() []string {
		result := base()
		for _, name := range helper.types() {
			if !slices.Contains(disabled, name) && !slices.Contains(result, name) {
				result = append(result, name)
			}
		}
		return result
	}
}
