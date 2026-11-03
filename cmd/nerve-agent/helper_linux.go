//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

const (
	helperUnitName = "nerve-agent-helper.service"
	helperUnitPath = "/etc/systemd/system/" + helperUnitName
	// helperStateDir holds helper.json and the replay register (root only).
	helperStateDir = "/var/lib/nerve-agent-helper"
	// helperRuntimeDir holds the socket: root-owned, group nerve-agent.
	helperRuntimeDir = "/run/nerve-agent-helper"
	helperSocketPath = helperRuntimeDir + "/helper.sock"
)

func helperSupported() bool { return true }

// helperSystemdUnit runs the helper as root without network access and with
// an empty capability bounding set. #92 adds the capabilities its handlers
// need. Group=nerve-agent makes systemd create the runtime directory and the
// socket with that group, so the agent can connect without CAP_CHOWN.
func helperSystemdUnit(executable string) string {
	return fmt.Sprintf(`# Installed by "nerve-agent service install --with-helper". Reinstall to update.
[Unit]
Description=NERVE Agent privileged helper
Documentation=https://github.com/Luis-0508/NERVE-App/blob/master/docs/AGENT_SETUP.md
Before=%s
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=%s helper
User=root
Group=%s
Restart=always
RestartSec=5
UMask=0077
RuntimeDirectory=nerve-agent-helper
RuntimeDirectoryMode=0750
StateDirectory=nerve-agent-helper
StateDirectoryMode=0700
NoNewPrivileges=true
CapabilityBoundingSet=
AmbientCapabilities=
PrivateNetwork=true
RestrictAddressFamilies=AF_UNIX
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`, unitName, systemdEscape(executable), serviceUser)
}

// installHelper pins the enrolled identity for the helper and installs its
// unit. It runs as root after the agent's state is prepared. The agent account
// can write its identity file, so an existing pin is changed only by an
// enrollment this installer made itself (enrolledNow).
func installHelper(executable string, id *identity, enrolledNow, noStart bool) error {
	if id == nil {
		return errors.New("the privileged helper needs an enrolled agent; install with --token-file first")
	}
	pin := newHelperPin(id)
	if previous, err := loadHelperPin(helperStateDir); err == nil && !enrolledNow {
		if previous.AgentID != pin.AgentID || !slices.ContainsFunc(previous.keys, func(key ed25519.PublicKey) bool { return key.Equal(id.coreKey) }) {
			return errors.New("the agent identity or its Core key changed since the helper was installed; re-enroll with --token-file and --force")
		}
		pin = previous
	} else if !enrolledNow {
		if helperInstalled() {
			// An installed helper without a usable pin: only a new enrollment
			// may establish trust again.
			return fmt.Errorf("the installed helper has no usable pin (%v); re-enroll with --token-file and --force", err)
		}
		fmt.Printf("The helper trusts Core key %s from the existing identity; compare it with NERVE before enabling privileged commands.\n",
			agentproto.CoreKeyID(id.coreKey))
	}
	if err := os.MkdirAll(helperStateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(helperStateDir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(helperStateDir, 0o700); err != nil {
		return err
	}
	if err := writeHelperPin(helperStateDir, pin); err != nil {
		return fmt.Errorf("write %s: %w", helperPinFileName, err)
	}
	if err := os.WriteFile(helperUnitPath, []byte(helperSystemdUnit(executable)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", helperUnitPath, err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", helperUnitName); err != nil {
		return err
	}
	fmt.Println("Installed", helperUnitPath, "(privileged helper for agent", id.AgentID+").")
	if noStart {
		return nil
	}
	return systemctl("restart", helperUnitName)
}

func helperInstalled() bool {
	_, err := os.Stat(helperUnitPath)
	return err == nil
}

func uninstallHelper(purge bool) error {
	if !helperInstalled() {
		return nil
	}
	_ = systemctl("disable", "--now", helperUnitName)
	if err := os.Remove(helperUnitPath); err != nil {
		return err
	}
	_ = systemctl("daemon-reload")
	fmt.Println("Removed", helperUnitPath+".")
	if purge {
		if err := os.RemoveAll(helperStateDir); err != nil {
			return err
		}
	}
	return nil
}

// writeHelperPin replaces helper.json atomically as a root-only file.
func writeHelperPin(dir string, pin helperPin) error {
	encoded, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "."+helperPinFileName+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
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
	return os.Rename(temporary.Name(), filepath.Join(dir, helperPinFileName))
}

// loadHelperPin refuses a pin file that a non-root account could have written.
func loadHelperPin(dir string) (helperPin, error) {
	path := filepath.Join(dir, helperPinFileName)
	for _, item := range []string{dir, path} {
		info, err := os.Stat(item)
		if err != nil {
			return helperPin{}, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return helperPin{}, fmt.Errorf("%s must be owned by root and not writable by others", item)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return helperPin{}, err
	}
	return parseHelperPin(raw)
}

func peerCredentials(connection net.Conn) (*syscall.Ucred, error) {
	unixConn, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, errors.New("not a Unix socket connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		credentials *syscall.Ucred
		credErr     error
	)
	if err := raw.Control(func(fd uintptr) {
		credentials, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return credentials, credErr
}

// peerUIDIn accepts a connection whose process runs as one of uids.
func peerUIDIn(uids ...uint32) func(net.Conn) error {
	return func(connection net.Conn) error {
		credentials, err := peerCredentials(connection)
		if err != nil {
			return err
		}
		for _, uid := range uids {
			if credentials.Uid == uid {
				return nil
			}
		}
		return fmt.Errorf("process %d runs as uid %d", credentials.Pid, credentials.Uid)
	}
}

// dialUnixHelper connects to socket and accepts only a helper whose process
// runs as serverUID.
func dialUnixHelper(socket string, serverUID uint32) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		connection, err := dialer.DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		if err := peerUIDIn(serverUID)(connection); err != nil {
			connection.Close()
			return nil, fmt.Errorf("the helper socket is not served by the expected account: %w", err)
		}
		return connection, nil
	}
}

func newLocalHelperClient(agentID string, logger *slog.Logger) privilegedRunner {
	return &helperClient{dial: dialUnixHelper(helperSocketPath, 0), agentID: agentID, logger: logger}
}

func runHelper(args []string) int {
	flags := flag.NewFlagSet("helper", flag.ContinueOnError)
	stateDir := flags.String("state-dir", helperStateDir, "directory with helper.json and the replay register")
	socket := flags.String("socket", helperSocketPath, "Unix socket the agent connects to")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	logger := newLogger(os.Stdout, new(slog.LevelVar))
	if os.Geteuid() != 0 {
		logger.Error("the privileged helper must run as root; install it with `nerve-agent service install --with-helper`")
		return 1
	}
	pin, err := loadHelperPin(*stateDir)
	if err != nil {
		logger.Error("helper trust anchor unusable", "error", err)
		return 1
	}
	replay, err := openReplayStore(filepath.Join(*stateDir, helperReplayFileName))
	if err != nil {
		logger.Error("replay register unusable", "error", err)
		return 1
	}
	account, err := user.Lookup(serviceUser)
	if err != nil {
		logger.Error("agent account not found", "user", serviceUser, "error", err)
		return 1
	}
	agentUID, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		logger.Error("agent account has an invalid uid", "error", err)
		return 1
	}
	_ = os.Remove(*socket)
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		logger.Error("helper socket unavailable", "socket", *socket, "error", err)
		return 1
	}
	if err := os.Chmod(*socket, 0o660); err != nil {
		listener.Close()
		logger.Error("helper socket permissions", "error", err)
		return 1
	}
	server := &helperServer{pin: pin, replay: replay, handlers: helperHandlers, logger: logger, now: time.Now,
		authorized: peerUIDIn(uint32(agentUID), 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("NERVE privileged helper started", "agent_id", pin.AgentID, "socket", *socket, "version", version,
		"allowlist", server.types())
	if err := server.serve(ctx, listener); err != nil {
		logger.Error("helper stopped", "error", err)
		return 1
	}
	logger.Info("helper stopped")
	return 0
}

// diagnoseHelper reports the helper unit, its channel and whether it serves
// this agent.
func diagnoseHelper(ctx context.Context, result *diagnosis, agentID string) {
	if !helperInstalled() {
		result.add("helper", "skip", "not installed; privileged commands fail closed (install with --with-helper)")
		return
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{helperRuntimeDir, 0o750}, {helperSocketPath, 0o660}} {
		info, err := os.Stat(item.path)
		if err != nil {
			result.add("helper", "fail", "%s: %v", item.path, err)
			return
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		var group *user.Group
		if ok {
			group, _ = user.LookupGroupId(strconv.FormatUint(uint64(stat.Gid), 10))
		}
		if !ok || stat.Uid != 0 || info.Mode().Perm() != item.mode || group == nil || group.Name != serviceUser {
			result.add("helper", "fail", "%s must be root:%s with mode %04o", item.path, serviceUser, item.mode)
			return
		}
	}
	client := &helperClient{dial: dialUnixHelper(helperSocketPath, 0), agentID: agentID}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := client.exchange(checkCtx, helperRequest{Op: "health"})
	switch {
	case err != nil:
		result.add("helper", "fail", "installed but not answering: %v", err)
	case response.Health == nil:
		result.add("helper", "fail", "unexpected answer: %s", response.Code)
	case agentID != "" && response.Health.AgentID != agentID:
		result.add("helper", "fail", "serves agent %s, not %s; run `service install --with-helper` again", response.Health.AgentID, agentID)
	case len(response.Health.Types) == 0:
		result.add("helper", "ok", "version %s, channel root:%s; no privileged command types enabled yet", response.Health.Version, serviceUser)
	default:
		result.add("helper", "ok", "version %s, channel root:%s; runs %v", response.Health.Version, serviceUser, response.Health.Types)
	}
}
