//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// runningAsService reports whether systemd started us. Linux services do not
// need a separate control handler: systemd sends SIGTERM, which ends ctx.
func runningAsService() bool { return false }

func runAsService(runOptions) int { return 1 }

func detectRunMode() string {
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("NOTIFY_SOCKET") != "" {
		return "systemd"
	}
	return "foreground"
}

// sdNotify sends a state change to systemd (Type=notify units). Errors are
// ignored: outside systemd there is no socket.
func sdNotify(state string) {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return
	}
	defer connection.Close()
	_, _ = connection.Write([]byte(state))
}

// watchdogInterval is half of WatchdogSec, or 0 when the watchdog is off.
func watchdogInterval() time.Duration {
	value, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	return time.Duration(value) * time.Microsecond / 2
}

// systemdUnit runs the agent unprivileged with a read-only view of the system.
// StartLimitIntervalSec=0 keeps restarting it after repeated failures (for
// example while Core is unreachable at boot).
func systemdUnit(executable, stateDir string) string {
	return fmt.Sprintf(`# Installed by "nerve-agent service install". Reinstall to update.
[Unit]
Description=NERVE Agent
Documentation=https://github.com/Luis-0508/NERVE-App/blob/master/docs/AGENT_SETUP.md
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=notify
NotifyAccess=main
ExecStart=%s run --state-dir %s
User=%s
Group=%s
Restart=always
RestartSec=5
WatchdogSec=180
TimeoutStopSec=%d
KillMode=mixed
UMask=0077
StateDirectory=nerve-agent
StateDirectoryMode=0700
NoNewPrivileges=true
CapabilityBoundingSet=
AmbientCapabilities=
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
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
`, systemdEscape(executable), systemdEscape(stateDir), serviceUser, serviceUser, int((stopGracePeriod + 5*time.Second).Seconds()))
}

// systemdEscape quotes a path for ExecStart.
func systemdEscape(value string) string {
	if !strings.ContainsAny(value, " \t\"'\\$%") {
		return value
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + replacer.Replace(value) + `"`
}

func requireAdministrator() error {
	if os.Geteuid() != 0 {
		return errors.New("run this command as root (sudo)")
	}
	return nil
}

// checkServiceBinary refuses binaries that a non-root account could replace,
// because systemd would run the replacement on the next start.
func checkServiceBinary(path string) error {
	for _, item := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(item)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s must be owned by root and not writable by others; install the binary to /usr/local/bin first (scripts/agent/install-linux.sh does this)", item)
		}
	}
	return nil
}

func ensureServiceUser() (*user.User, error) {
	if account, err := user.Lookup(serviceUser); err == nil {
		return account, nil
	}
	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(shell); err != nil {
		shell = "/sbin/nologin"
	}
	output, err := exec.Command("useradd", "--system", "--user-group", "--home-dir", machineStateDir(), "--no-create-home", "--shell", shell, serviceUser).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("create user %s: %v: %s", serviceUser, err, strings.TrimSpace(string(output)))
	}
	fmt.Println("Created system user", serviceUser+".")
	return user.Lookup(serviceUser)
}

func systemctl(args ...string) error {
	output, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func installService(options installOptions) error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemd was not found; run the agent under your init system manually (see docs/AGENT_SETUP.md)")
	}
	if filepath.Clean(options.StateDir) != machineStateDir() {
		return fmt.Errorf("the systemd unit uses %s; --state-dir cannot be changed", machineStateDir())
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if executable, err = filepath.EvalSymlinks(executable); err != nil {
		return err
	}
	if err := checkServiceBinary(executable); err != nil {
		return err
	}
	account, err := ensureServiceUser()
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	if err := os.MkdirAll(options.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chown(options.StateDir, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(options.StateDir, 0o700); err != nil {
		return err
	}
	id, err := prepareServiceState(options)
	if err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit(executable, options.StateDir)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", unitPath, err)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unitName); err != nil {
		return err
	}
	fmt.Println("Installed", unitPath, "(user", serviceUser+").")
	if id == nil {
		fmt.Println("The service is installed but not enrolled. Enroll with: nerve-agent service install --token-file <file>.")
		return nil
	}
	if options.NoStart {
		return nil
	}
	state, err := controlService("restart")
	if err != nil {
		return err
	}
	fmt.Println("Service", state+".")
	return nil
}

func uninstallService(stateDir string, purge bool) error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); err == nil {
		_ = systemctl("disable", "--now", unitName)
		if err := os.Remove(unitPath); err != nil {
			return err
		}
		_ = systemctl("daemon-reload")
		fmt.Println("Removed", unitPath+".")
	} else {
		fmt.Println("Service is not installed.")
	}
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			return err
		}
		fmt.Println("Removed", stateDir+". Revoke or remove this agent in NERVE. The system user", serviceUser, "was kept.")
	} else {
		fmt.Println("The identity in", stateDir, "was kept; reinstalling reuses it. Use --purge to delete it.")
	}
	return nil
}

func controlService(action string) (string, error) {
	if _, err := os.Stat(unitPath); err != nil {
		return "not installed", nil
	}
	if action != "status" {
		if err := systemctl(action, unitName); err != nil {
			return "", err
		}
	}
	output, _ := exec.Command("systemctl", "is-active", unitName).Output()
	state := strings.TrimSpace(string(output))
	if state == "active" {
		state = "running"
	}
	return state, nil
}
