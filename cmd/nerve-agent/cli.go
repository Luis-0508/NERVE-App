package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

const usage = `Usage:
  nerve-agent                 run discovery and/or management (default)
  nerve-agent enroll          enroll this host with a one-time token
  nerve-agent status          show the local identity and verify it with Core
  nerve-agent version         print the agent version

Environment:
  NERVE_AGENT_API_URL            Core API base URL, e.g. https://nerve.example/api/v1
  NERVE_AGENT_STATE_DIR          identity directory (default: per-OS private location)
  NERVE_AGENT_ENROLLMENT_TOKEN   one-time token; enrolls automatically on first run
  NERVE_DISCOVERY_AGENT_TOKEN    shared local discovery token (legacy discovery worker)
`

func runCLI(args []string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "run":
		return runAgent(logger)
	case "enroll":
		return runEnroll(args)
	case "status":
		return runStatus(args)
	case "version":
		fmt.Println(version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		return 2
	}
}

func runEnroll(args []string) int {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	apiURL := flags.String("api-url", os.Getenv("NERVE_AGENT_API_URL"), "Core API base URL ending in /api/v1")
	tokenFile := flags.String("token-file", "", "file containing the one-time enrollment token")
	stateDir := flags.String("state-dir", defaultStateDir(), "directory for the agent identity")
	force := flags.Bool("force", false, "replace an existing local identity")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	token := strings.TrimSpace(os.Getenv("NERVE_AGENT_ENROLLMENT_TOKEN"))
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read token file:", err)
			return 1
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "Provide the token with --token-file or NERVE_AGENT_ENROLLMENT_TOKEN. Tokens are not accepted as command-line arguments because other local users can read process arguments.")
		return 2
	}
	if existing, err := loadIdentity(*stateDir); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "This host is already enrolled as agent %s (%s). Use --force with a re-enrollment token to replace the key.\n", existing.AgentID, *stateDir)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id, err := enroll(ctx, *apiURL, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "enrollment failed:", err)
		return 1
	}
	if err := saveIdentity(*stateDir, id); err != nil {
		fmt.Fprintln(os.Stderr, "enrolled, but the identity could not be saved:", err)
		fmt.Fprintln(os.Stderr, "Revoke agent", id.AgentID, "in NERVE and enroll again.")
		return 1
	}
	fmt.Printf("Enrolled as agent %s\nKey fingerprint (compare in NERVE): %s\nPinned NERVE Core key: %s\nIdentity: %s\n",
		id.AgentID, id.Fingerprint, agentproto.CoreKeyID(id.coreKey), *stateDir)
	fmt.Println("The enrollment token is now used up; remove it from any configuration.")
	return 0
}

func runStatus(args []string) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	stateDir := flags.String("state-dir", defaultStateDir(), "directory for the agent identity")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	id, err := loadIdentity(*stateDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "not enrolled:", err)
		return 1
	}
	apiURL := managementAPIURL(id)
	fmt.Printf("Agent:       %s\nCredential:  %s\nFingerprint: %s\nCore:        %s\nCore key:    %s (pinned)\n",
		id.AgentID, id.CredentialID, id.Fingerprint, apiURL, agentproto.CoreKeyID(id.coreKey))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var response agentproto.IdentityResponse
	if _, err := newManagementClient(apiURL, id).post(ctx, "/agent/identity", struct{}{}, &response); err != nil {
		fmt.Fprintln(os.Stderr, "Core check failed:", err)
		return 1
	}
	fmt.Println("Core accepts this identity, and its response is signed by the pinned Core key.")
	return 0
}

// managementAPIURL prefers an explicit NERVE_AGENT_API_URL (for example after
// Core moved) over the URL recorded at enrollment.
func managementAPIURL(id *identity) string {
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_API_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return id.APIURL
}

func runAgent(logger *slog.Logger) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	stateDir := defaultStateDir()
	id, err := loadIdentity(stateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.Error("agent identity unusable", "error", err, "state_dir", stateDir)
		return 1
	}
	if id == nil {
		if token := strings.TrimSpace(os.Getenv("NERVE_AGENT_ENROLLMENT_TOKEN")); token != "" {
			enrollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			id, err = enroll(enrollCtx, envOr("NERVE_AGENT_API_URL", "http://localhost:3000/api/v1"), token)
			cancel()
			if err != nil {
				logger.Error("automatic enrollment failed", "error", err)
				return 1
			}
			if err := saveIdentity(stateDir, id); err != nil {
				logger.Error("enrolled, but the identity could not be saved; revoke the agent and enroll again", "agent_id", id.AgentID, "error", err)
				return 1
			}
			logger.Info("agent enrolled", "agent_id", id.AgentID, "fingerprint", id.Fingerprint, "state_dir", stateDir)
		}
	}

	discoveryToken := strings.TrimSpace(os.Getenv("NERVE_DISCOVERY_AGENT_TOKEN"))
	if discoveryToken == "change-me" {
		discoveryToken = ""
	}
	if id == nil && discoveryToken == "" {
		logger.Error("nothing to run: enroll this agent for management or set NERVE_DISCOVERY_AGENT_TOKEN for discovery")
		return 1
	}

	var workers sync.WaitGroup
	failed := make(chan error, 2)
	if discoveryToken != "" {
		apiURL := strings.TrimRight(envOr("NERVE_AGENT_API_URL", "http://localhost:3000/api/v1"), "/")
		discoveryAgent := newDiscoveryAgent(apiURL, discoveryToken, logger)
		logger.Info("NERVE discovery worker started", "id", discoveryAgent.id, "api", apiURL, "version", version)
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := discoveryAgent.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				failed <- err
			}
		}()
	}
	if id != nil {
		apiURL := managementAPIURL(id)
		m := &manager{client: newManagementClient(apiURL, id), agentID: id.AgentID, logger: logger, heartbeatInterval: 30 * time.Second,
			capabilities: supportedCapabilities, facts: collectHostFacts}
		logger.Info("NERVE management agent started", "agent_id", id.AgentID, "fingerprint", id.Fingerprint, "api", apiURL, "version", version)
		workers.Add(1)
		go func() {
			defer workers.Done()
			m.run(ctx)
		}()
	}
	go func() {
		workers.Wait()
		close(failed)
	}()
	if err, ok := <-failed; ok && err != nil {
		logger.Error("agent stopped", "error", err)
		return 1
	}
	return 0
}
