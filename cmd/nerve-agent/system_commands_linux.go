//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Services the helper never restarts or stops: NERVE itself and the plumbing
// systemd, logins and the bus depend on.
var protectedServices = []string{"nerve-agent.service", "nerve-agent-helper.service", "dbus.service", "dbus-broker.service",
	"systemd-journald.service", "systemd-logind.service", "systemd-udevd.service", "polkit.service"}

// Services that may be restarted but never stopped, because stopping them can
// cut the operator off the host.
var stopProtectedServices = []string{"ssh.service", "sshd.service", "systemd-networkd.service", "NetworkManager.service",
	"systemd-resolved.service"}

// Processes (by /proc/<pid>/stat name, as process.list reports it) the helper
// never ends. Processes of protected services are also refused by their
// systemd unit (processUnit), whatever their name.
var protectedProcesses = []string{"systemd", "init", "kthreadd", "nerve-agent", "systemd-journal", "systemd-logind",
	"systemd-udevd", "dbus-daemon", "dbus-broker", "sshd", "polkitd", "NetworkManager", "systemd-network", "systemd-resolve"}

// serviceProtected applies both protection lists: ending a stop-protected
// service's process is a stop as well.
func serviceProtected(unit, action string) bool {
	return protectedName(protectedServices, unit) || (action == "stop" && protectedName(stopProtectedServices, unit))
}

// unitProperty reads one "Key=value" line from systemctl show.
func unitProperty(properties, key string) string {
	for _, line := range strings.Split(properties, "\n") {
		if value, found := strings.CutPrefix(line, key+"="); found {
			return value
		}
	}
	return ""
}

// processUnit returns the systemd service unit whose cgroup holds pid, or "".
func processUnit(pid uint32) string {
	raw, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		segments := strings.Split(parts[2], "/")
		for index := len(segments) - 1; index >= 0; index-- {
			if strings.HasSuffix(segments[index], ".service") {
				return segments[index]
			}
		}
	}
	return ""
}

func runSystemctl(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "systemctl", args...)
	command.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	text := strings.TrimSpace(output.String())
	if len(text) > 500 {
		text = text[:500]
	}
	return text, err
}

func controlManagedService(ctx context.Context, name, action string) (map[string]any, error) {
	unit := name
	if !strings.Contains(unit, ".") {
		unit += ".service"
	}
	if !strings.HasSuffix(unit, ".service") {
		return nil, &helperError{"invalid_parameters", "Only service units can be controlled."}
	}
	if serviceProtected(unit, action) {
		return nil, refusedService(unit)
	}
	properties, err := runSystemctl(ctx, "show", "--property=LoadState,Id", "--", unit)
	if err != nil || !strings.Contains(properties, "LoadState=loaded") {
		return nil, &helperError{"unknown_service", "No service " + unit + " is installed."}
	}
	// systemctl follows alias links (for example
	// dbus-org.freedesktop.login1.service to systemd-logind.service). Act only
	// on canonical names, so the approved name is the unit that is controlled
	// and the protection lists cannot be bypassed.
	if canonical := unitProperty(properties, "Id"); canonical != unit {
		if serviceProtected(canonical, action) {
			return nil, refusedService(canonical)
		}
		return nil, &helperError{"service_alias", unit + " is an alias of " + canonical + "; request " + canonical + " instead."}
	}
	if output, err := runSystemctl(ctx, action, "--", unit); err != nil {
		return nil, fmt.Errorf("systemctl %s %s: %v: %s", action, unit, err, output)
	}
	state, _ := runSystemctl(ctx, "is-active", "--", unit)
	return map[string]any{"service": unit, "action": action, "state": state}, nil
}

// processStat returns the name and parent PID from /proc/<pid>/stat.
func processStat(pid uint32) (string, uint64, error) {
	raw, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/stat")
	if err != nil {
		return "", 0, err
	}
	open, closing := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
	fields := strings.Fields(string(raw[closing+1:]))
	if open < 0 || closing < open || len(fields) < 2 {
		return "", 0, errors.New("unreadable process status")
	}
	parent, _ := strconv.ParseUint(fields[1], 10, 32)
	return string(raw[open+1 : closing]), parent, nil
}

// sameProcessName compares with the kernel's name, which is cut to 15 bytes.
func sameProcessName(expected, actual string) bool {
	return expected == actual || (len(expected) > 15 && expected[:15] == actual)
}

// terminateProcess pins the process with a pidfd before checking its name, so
// the signal reaches exactly the process that was checked. It sends SIGTERM,
// then SIGKILL if the process is still running after ten seconds.
func terminateProcess(ctx context.Context, pid uint32, name string) (map[string]any, error) {
	if pid <= 2 || int(pid) == os.Getpid() || int(pid) == os.Getppid() {
		return nil, refusedProcess(name)
	}
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return nil, &helperError{"unknown_process", "No process with PID " + strconv.FormatUint(uint64(pid), 10) + " is running."}
	}
	defer unix.Close(fd)
	actual, parent, err := processStat(pid)
	switch {
	case err != nil:
		return nil, &helperError{"unknown_process", "The process exited."}
	case parent == 2:
		return nil, refusedProcess(actual) // A kernel thread.
	case !sameProcessName(name, actual):
		return nil, processNameMismatch(name, actual)
	case protectedName(protectedProcesses, actual):
		return nil, refusedProcess(actual)
	case serviceProtected(processUnit(pid), "stop"):
		return nil, refusedProcess(actual + " (" + processUnit(pid) + ")")
	}
	signal := "SIGTERM"
	if err := unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil {
		return nil, fmt.Errorf("signal process %d: %w", pid, err)
	}
	exited := waitPidfd(ctx, fd, 10*time.Second)
	if !exited && ctx.Err() == nil {
		signal = "SIGKILL"
		if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return nil, fmt.Errorf("kill process %d: %w", pid, err)
		}
		exited = waitPidfd(ctx, fd, 5*time.Second)
	}
	return map[string]any{"pid": pid, "name": actual, "signal": signal, "exited": exited}, nil
}

// waitPidfd reports whether the process exited within limit.
func waitPidfd(ctx context.Context, fd int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if count, err := unix.Poll(descriptors, 200); err == nil && count > 0 {
			return true
		}
	}
	return false
}

// scheduleShutdown hands the reboot or power-off to a transient systemd timer,
// so it happens even if the helper restarts in between.
func scheduleShutdown(ctx context.Context, reboot bool) (map[string]any, error) {
	action := "poweroff"
	if reboot {
		action = "reboot"
	}
	command := exec.CommandContext(ctx, "systemd-run", "--unit=nerve-power-action", "--on-active="+strconv.Itoa(int(shutdownDelay.Seconds())),
		"--timer-property=AccuracySec=1s", "--", "systemctl", action)
	command.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	if output, err := command.CombinedOutput(); err != nil {
		text := strings.TrimSpace(string(output))
		if strings.Contains(text, "already") {
			return nil, &helperError{"already_scheduled", "A reboot or shutdown is already scheduled."}
		}
		return nil, fmt.Errorf("schedule %s: %v: %s", action, err, text)
	}
	return map[string]any{"action": action, "scheduled": true, "inSeconds": int(shutdownDelay.Seconds())}, nil
}
