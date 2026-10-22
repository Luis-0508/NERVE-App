package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// serviceName is the Windows service name and the systemd unit base name.
	serviceName        = "NERVEAgent"
	serviceDisplayName = "NERVE Agent"
	serviceDescription = "NERVE remote-management agent: heartbeats, inventory and authorized remote commands."
	// serviceAccount is the Windows virtual service account. It has no
	// password and no administrative rights.
	serviceAccount = `NT SERVICE\NERVEAgent`
	// serviceUser is the unprivileged Linux account the systemd unit runs as.
	serviceUser = "nerve-agent"
	unitName    = "nerve-agent.service"
	unitPath    = "/etc/systemd/system/" + unitName
	// stopGracePeriod bounds a graceful stop: the final heartbeat and the
	// result of an interrupted command.
	stopGracePeriod = 15 * time.Second
)

const serviceUsage = `Usage:
  nerve-agent service install   [--api-url URL] [--token-file FILE] [--ca-file FILE] [--state-dir DIR] [--no-start] [--force]
  nerve-agent service uninstall [--purge]
  nerve-agent service start | stop | restart | status

install registers the agent as a Windows service (virtual account
NT SERVICE\NERVEAgent) or a systemd unit (user nerve-agent), with automatic
restart after crashes. It must run as Administrator or root. With --token-file
it also enrolls the host; --force replaces an existing identity (re-enrollment).
uninstall removes the service; --purge also deletes the local identity. Revoke
or remove the agent in NERVE as well, so its key can no longer be used.
`

// installOptions are shared by both platform installers.
type installOptions struct {
	APIURL    string
	TokenFile string
	CAFile    string
	StateDir  string
	NoStart   bool
	Force     bool
}

func runService(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, serviceUsage)
		return 2
	}
	action, args := args[0], args[1:]
	switch action {
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		options := installOptions{}
		flags.StringVar(&options.APIURL, "api-url", os.Getenv("NERVE_AGENT_API_URL"), "Core API base URL ending in /api/v1")
		flags.StringVar(&options.TokenFile, "token-file", "", "one-time enrollment token file (enrolls the host)")
		flags.StringVar(&options.CAFile, "ca-file", "", "PEM file with an additional CA that issued Core's certificate")
		flags.StringVar(&options.StateDir, "state-dir", machineStateDir(), "service state directory")
		flags.BoolVar(&options.NoStart, "no-start", false, "do not start the service after installing")
		flags.BoolVar(&options.Force, "force", false, "replace an existing identity with a re-enrollment token")
		if err := flags.Parse(args); err != nil {
			return 2
		}
		if err := installService(options); err != nil {
			fmt.Fprintln(os.Stderr, "install failed:", err)
			return 1
		}
		return 0
	case "uninstall":
		flags := flag.NewFlagSet("service uninstall", flag.ContinueOnError)
		purge := flags.Bool("purge", false, "also delete the local identity, configuration and logs")
		stateDir := flags.String("state-dir", machineStateDir(), "service state directory")
		if err := flags.Parse(args); err != nil {
			return 2
		}
		if err := uninstallService(*stateDir, *purge); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall failed:", err)
			return 1
		}
		return 0
	case "start", "stop", "restart", "status":
		state, err := controlService(action)
		if err != nil {
			fmt.Fprintln(os.Stderr, action+" failed:", err)
			return 1
		}
		fmt.Println(state)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown service action %q\n\n%s", action, serviceUsage)
		return 2
	}
}

// prepareServiceState writes the configuration and, with a token, enrolls the
// host. It runs after the service account exists, so the state directory can
// grant it access.
func prepareServiceState(options installOptions) (*identity, error) {
	existing, err := loadIdentity(options.StateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		if options.TokenFile == "" || !options.Force {
			return nil, fmt.Errorf("existing identity in %s is unusable (%v); re-enroll with --token-file and --force", options.StateDir, err)
		}
		existing = nil
	}
	config, err := loadConfig(options.StateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if options.APIURL != "" {
		config.APIURL = strings.TrimRight(strings.TrimSpace(options.APIURL), "/")
	}
	if config.APIURL == "" && existing != nil {
		config.APIURL = existing.APIURL
	}
	if config.APIURL == "" {
		return nil, errors.New("--api-url is required for a new installation")
	}
	if err := validateAPIURL(config.APIURL); err != nil {
		return nil, err
	}
	if options.CAFile != "" {
		// The service account cannot read the administrator's copy (a user
		// profile, or /root and /home under ProtectHome), so keep a private copy.
		if _, err := newTransport(options.CAFile); err != nil {
			return nil, err
		}
		pem, err := os.ReadFile(options.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		if err := writePrivateFile(options.StateDir, caFileName, pem); err != nil {
			return nil, fmt.Errorf("copy CA file: %w", err)
		}
		config.CAFile = filepath.Join(options.StateDir, caFileName)
	}
	if err := saveConfig(options.StateDir, config); err != nil {
		return nil, fmt.Errorf("write configuration: %w", err)
	}
	if strings.HasPrefix(config.APIURL, "http://") {
		fmt.Println("Warning: the Core URL uses plain HTTP. Requests are authenticated in both directions, but results and host facts are readable on the network. Use HTTPS outside a trusted network.")
	}
	if options.TokenFile == "" {
		return existing, nil
	}
	if existing != nil && !options.Force {
		fmt.Printf("Already enrolled as agent %s; the token was not used. Use --force with a re-enrollment token to replace the key.\n", existing.AgentID)
		return existing, nil
	}
	raw, err := os.ReadFile(options.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	transport, err := newTransport(config.CAFile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id, err := enrollWith(ctx, config.APIURL, strings.TrimSpace(string(raw)), transport)
	if err != nil {
		return nil, fmt.Errorf("enrollment: %w", err)
	}
	if err := saveIdentity(options.StateDir, id); err != nil {
		return nil, fmt.Errorf("enrolled as %s, but the identity could not be saved (revoke it in NERVE): %w", id.AgentID, err)
	}
	fmt.Printf("Enrolled as agent %s\nKey fingerprint (compare in NERVE): %s\n", id.AgentID, id.Fingerprint)
	fmt.Println(id.tokenNotice())
	if notice := id.approvalNotice(); notice != "" {
		fmt.Println(notice)
	}
	return id, nil
}
