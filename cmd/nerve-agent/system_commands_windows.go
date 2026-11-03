//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Services the helper never restarts or stops: NERVE itself and core Windows
// services whose loss breaks the session, security or remote access.
var protectedServices = []string{serviceName, helperServiceName, "RpcSs", "RpcEptMapper", "DcomLaunch", "LSM", "SamSs",
	"EventLog", "PlugPlay", "Power", "BrokerInfrastructure", "SystemEventsBroker", "ProfSvc", "gpsvc", "Winmgmt",
	"WinDefend", "mpssvc", "BFE", "nsi", "CryptSvc"}

// Services that may be restarted but never stopped, because stopping them can
// cut the operator off the host.
var stopProtectedServices = []string{"TermService", "Dhcp", "Dnscache", "LanmanWorkstation", "WinRM", "sshd"}

// Processes (by image name, as process.list reports it) the helper never ends.
var protectedProcesses = []string{"System", "Registry", "smss.exe", "csrss.exe", "wininit.exe", "winlogon.exe",
	"services.exe", "lsass.exe", "lsaiso.exe", "svchost.exe", "fontdrvhost.exe", "dwm.exe", "MsMpEng.exe",
	"MemCompression", "Secure System", "nerve-agent.exe"}

func controlManagedService(_ context.Context, name, action string) (map[string]any, error) {
	if protectedName(protectedServices, name) || (action == "stop" && protectedName(stopProtectedServices, name)) {
		return nil, refusedService(name)
	}
	manager, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		return nil, &helperError{"unknown_service", "No service " + name + " is installed."}
	}
	defer service.Close()
	state, err := stopService(service)
	if err != nil {
		return nil, err
	}
	if action == "restart" {
		if err := service.Start(); err != nil {
			return nil, fmt.Errorf("start service %s: %w", name, err)
		}
		if state, err = waitForState(service, svc.Running); err != nil {
			return nil, err
		}
	}
	return map[string]any{"service": name, "action": action, "state": state}, nil
}

// terminateProcess opens the process first, so the handle pins exactly the
// process whose image name is then checked and ended.
func terminateProcess(_ context.Context, pid uint32, name string) (map[string]any, error) {
	if pid <= 4 || pid == uint32(os.Getpid()) {
		return nil, refusedProcess(name)
	}
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return nil, &helperError{"unknown_process", "No process with PID " + strconv.FormatUint(uint64(pid), 10) + " can be opened."}
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return nil, fmt.Errorf("read process image: %w", err)
	}
	actual := filepath.Base(windows.UTF16ToString(buffer[:size]))
	switch {
	case !protectedName([]string{name}, actual):
		return nil, processNameMismatch(name, actual)
	case protectedName(protectedProcesses, actual):
		return nil, refusedProcess(actual)
	}
	if err := windows.TerminateProcess(process, 1); err != nil {
		return nil, fmt.Errorf("end process %d: %w", pid, err)
	}
	event, _ := windows.WaitForSingleObject(process, 5000)
	return map[string]any{"pid": pid, "name": actual, "exited": event == windows.WAIT_OBJECT_0}, nil
}

// scheduleShutdown asks Windows to restart or shut down after shutdownDelay,
// with a notice to signed-in users. LocalSystem holds SeShutdownPrivilege but
// must enable it first.
func scheduleShutdown(_ context.Context, reboot bool) (map[string]any, error) {
	if err := enableShutdownPrivilege(); err != nil {
		return nil, fmt.Errorf("enable shutdown privilege: %w", err)
	}
	action, verb := "poweroff", "shuts down"
	if reboot {
		action, verb = "reboot", "restarts"
	}
	message, _ := windows.UTF16PtrFromString(fmt.Sprintf("This computer %s in %d seconds. An administrator approved this in NERVE.",
		verb, int(shutdownDelay.Seconds())))
	const plannedOther = 0x80000000 // SHTDN_REASON_FLAG_PLANNED | SHTDN_REASON_MAJOR_OTHER
	if err := windows.InitiateSystemShutdownEx(nil, message, uint32(shutdownDelay.Seconds()), true, reboot, plannedOther); err != nil {
		if err == windows.ERROR_SHUTDOWN_IN_PROGRESS {
			return nil, &helperError{"already_scheduled", "A restart or shutdown is already in progress."}
		}
		return nil, fmt.Errorf("schedule %s: %w", action, err)
	}
	return map[string]any{"action": action, "scheduled": true, "inSeconds": int(shutdownDelay.Seconds())}, nil
}

func enableShutdownPrivilege() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	name, _ := windows.UTF16PtrFromString("SeShutdownPrivilege")
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return err
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1}
	privileges.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	return windows.AdjustTokenPrivileges(token, false, &privileges, uint32(unsafe.Sizeof(privileges)), nil, nil)
}
