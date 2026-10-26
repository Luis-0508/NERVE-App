package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

const usage = `Usage:
  nerve-agent [run]           run discovery and/or management (default)
  nerve-agent enroll          enroll this host with a one-time token
  nerve-agent status          show the local identity and verify it with Core
  nerve-agent diagnose        check configuration, identity, network, TLS and Core
  nerve-agent service ...     install, uninstall, start, stop or query the service
  nerve-agent version         print the agent version (also --version)
  nerve-agent verify-release DIR
                              verify a downloaded release against the pinned release key

Environment (overrides config.json in the state directory):
  NERVE_AGENT_API_URL            Core API base URL, e.g. https://nerve.example/api/v1
  NERVE_AGENT_STATE_DIR          identity directory (default: per-OS private location)
  NERVE_AGENT_CA_FILE            additional PEM CA for Core's TLS certificate
  NERVE_AGENT_LOG_LEVEL          debug, info, warn or error
  NERVE_AGENT_ENROLLMENT_TOKEN   one-time token; enrolls automatically on first run
  NERVE_DISCOVERY_AGENT_TOKEN    shared local discovery token (legacy discovery worker)
`

// identityPollInterval is how often a running agent checks whether its
// identity file was replaced (re-enrollment while the service runs).
var identityPollInterval = 15 * time.Second

// runOptions configure the long-running agent.
type runOptions struct {
	stateDir string
	runMode  string
	// logToFile writes logs to <state dir>/logs (Windows services have no
	// stdout collector).
	logToFile bool
}

func runCLI(args []string) int {
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	} else if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		command, args = "version", args[1:]
	}
	switch command {
	case "run":
		return runCommand(args)
	case "enroll":
		return runEnroll(args)
	case "status":
		return runStatus(args)
	case "diagnose":
		return runDiagnose(args)
	case "service":
		return runService(args)
	case "version":
		fmt.Println(version)
		return 0
	case "verify-release":
		return runVerifyRelease(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		return 2
	}
}

func runCommand(args []string) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := flags.String("state-dir", defaultStateDir(), "directory for the agent identity and configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	options := runOptions{stateDir: *stateDir, runMode: detectRunMode()}
	if runningAsService() {
		options.logToFile = true
		return runAsService(options)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAgent(ctx, options)
}

func runEnroll(args []string) int {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	apiURL := flags.String("api-url", "", "Core API base URL ending in /api/v1 (default: configuration)")
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
	existing, err := loadIdentity(*stateDir)
	if err == nil && !*force {
		fmt.Fprintf(os.Stderr, "This host is already enrolled as agent %s (%s). Use --force with a re-enrollment token to replace the key.\n", existing.AgentID, *stateDir)
		return 1
	}
	config, err := resolveSettings(*stateDir, existing)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	if *apiURL != "" {
		config.APIURL = strings.TrimRight(strings.TrimSpace(*apiURL), "/")
	}
	transport, err := newTransport(config.CAFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "TLS configuration:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id, err := enrollWith(ctx, config.APIURL, token, transport)
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
	fmt.Println(id.tokenNotice())
	if notice := id.approvalNotice(); notice != "" {
		fmt.Println(notice)
	}
	if existing != nil {
		fmt.Println("A running agent picks up the new identity within", identityPollInterval.String()+".")
	}
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
	config, err := resolveSettings(*stateDir, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	fmt.Printf("Agent:       %s\nCredential:  %s\nFingerprint: %s\nCore:        %s\nCore key:    %s (pinned)\nState dir:   %s\n",
		id.AgentID, id.CredentialID, id.Fingerprint, config.APIURL, agentproto.CoreKeyID(id.coreKey), *stateDir)
	if state, err := controlService("status"); err == nil {
		fmt.Printf("Service:     %s\n", state)
	}
	transport, err := newTransport(config.CAFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "TLS configuration:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var response agentproto.IdentityResponse
	if _, err := newManagementClientWith(config.APIURL, id, transport).post(ctx, "/agent/identity", struct{}{}, &response); err != nil {
		fmt.Fprintln(os.Stderr, "Core check failed:", err)
		return 1
	}
	fmt.Println("Core accepts this identity, and its response is signed by the pinned Core key.")
	if response.Status == "pending" {
		fmt.Println("This agent waits for approval in NERVE (Settings → Management agents) and receives no commands until then.")
	}
	return 0
}

// runAgent runs until ctx ends. It returns a process exit code.
func runAgent(ctx context.Context, options runOptions) int {
	level := new(slog.LevelVar)
	var output io.Writer = os.Stdout
	if options.logToFile {
		file, err := openRotatingFile(filepath.Join(options.stateDir, "logs"))
		if err != nil {
			return 1
		}
		defer file.Close()
		output = file
	}
	logger := newLogger(output, level)

	id, err := loadIdentity(options.stateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.Error("agent identity unusable", "error", err, "state_dir", options.stateDir)
		return 1
	}
	config, err := resolveSettings(options.stateDir, id)
	if err != nil {
		logger.Error("agent configuration invalid", "error", err, "state_dir", options.stateDir)
		return 1
	}
	level.Set(config.LogLevel)
	transport, err := newTransport(config.CAFile)
	if err != nil {
		logger.Error("TLS configuration invalid", "error", err)
		return 1
	}
	if id == nil {
		if token := strings.TrimSpace(os.Getenv("NERVE_AGENT_ENROLLMENT_TOKEN")); token != "" {
			apiURL := config.APIURL
			if apiURL == "" {
				apiURL = "http://localhost:3000/api/v1"
			}
			enrollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			id, err = enrollWith(enrollCtx, apiURL, token, transport)
			cancel()
			if err != nil {
				logger.Error("automatic enrollment failed", "error", err)
				return 1
			}
			if err := saveIdentity(options.stateDir, id); err != nil {
				logger.Error("enrolled, but the identity could not be saved; revoke the agent and enroll again", "agent_id", id.AgentID, "error", err)
				return 1
			}
			logger.Info("agent enrolled", "agent_id", id.AgentID, "fingerprint", id.Fingerprint, "state_dir", options.stateDir,
				"pending_approval", id.pendingApproval)
			if config.APIURL == "" {
				config.APIURL = apiURL
			}
		}
	}

	discoveryToken := strings.TrimSpace(os.Getenv("NERVE_DISCOVERY_AGENT_TOKEN"))
	if discoveryToken == "change-me" {
		discoveryToken = ""
	}
	if id == nil && discoveryToken == "" {
		logger.Error("nothing to run: enroll this agent for management or set NERVE_DISCOVERY_AGENT_TOKEN for discovery", "state_dir", options.stateDir)
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
		workers.Add(1)
		go func() {
			defer workers.Done()
			runManagement(ctx, managementOptions{stateDir: options.stateDir, runMode: options.runMode, settings: config,
				transport: transport, logger: logger}, id)
		}()
	}
	sdNotify("READY=1")
	if interval := watchdogInterval(); interval > 0 {
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					sdNotify("WATCHDOG=1")
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(failed)
	}()
	err, ok := <-failed
	sdNotify("STOPPING=1")
	if ok && err != nil {
		logger.Error("agent stopped", "error", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}

type managementOptions struct {
	stateDir  string
	runMode   string
	settings  settings
	transport http.RoundTripper
	logger    *slog.Logger
}

// runManagement runs the management loops and restarts them with the new key
// when the identity file is replaced (for example `enroll --force` while the
// service runs). On shutdown it reports a final "stopping" heartbeat.
func runManagement(ctx context.Context, options managementOptions, id *identity) {
	startedAt := time.Now()
	for {
		apiURL := options.settings.APIURL
		if apiURL == "" {
			apiURL = id.APIURL
		}
		m := &manager{client: newManagementClientWith(apiURL, id, options.transport), agentID: id.AgentID, logger: options.logger,
			heartbeatInterval: 30 * time.Second, capabilities: capabilitiesWithout(options.settings.DisabledCapabilities),
			facts: collectHostFacts, runtime: runtimeReporter(options.runMode, startedAt)}
		options.logger.Info("NERVE management agent started", "agent_id", id.AgentID, "fingerprint", id.Fingerprint, "api", apiURL,
			"version", version, "run_mode", options.runMode)
		loopCtx, stopLoops := context.WithCancel(ctx)
		go watchIdentity(loopCtx, options.stateDir, id.CredentialID, stopLoops, options.logger)
		m.run(loopCtx)
		stopLoops()
		if ctx.Err() != nil {
			m.reportStopping()
			return
		}
		for ctx.Err() == nil {
			next, err := loadIdentity(options.stateDir)
			if err == nil {
				options.logger.Info("agent identity changed; restarting management with the new credential", "agent_id", next.AgentID,
					"fingerprint", next.Fingerprint)
				id = next
				break
			}
			options.logger.Error("agent identity unreadable; retrying", "error", err, "state_dir", options.stateDir)
			if !sleepContext(ctx, identityPollInterval) {
				return
			}
		}
	}
}

// watchIdentity calls changed when the identity file holds a different
// credential than the running one.
func watchIdentity(ctx context.Context, dir, credentialID string, changed func(), logger *slog.Logger) {
	path := filepath.Join(dir, identityFileName)
	last, _ := os.Stat(path)
	for sleepContext(ctx, identityPollInterval) {
		info, err := os.Stat(path)
		if err != nil || (last != nil && info.ModTime().Equal(last.ModTime()) && info.Size() == last.Size()) {
			continue
		}
		last = info
		next, err := loadIdentity(dir)
		if err != nil {
			logger.Warn("agent identity file changed but cannot be loaded", "error", err)
			continue
		}
		if next.CredentialID != credentialID {
			changed()
			return
		}
	}
}
