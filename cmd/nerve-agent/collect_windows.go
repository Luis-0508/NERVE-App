//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGetProcessMemoryInfo = kernel32.NewProc("K32GetProcessMemoryInfo")
)

func platformCapabilities() []string {
	return []string{agentproto.CapabilitySystemInfo, agentproto.CapabilitySystemMetrics, agentproto.CapabilityProcessList,
		agentproto.CapabilityServiceList, agentproto.CapabilitySoftwareList}
}

func kernelVersion() string {
	major, minor, build := windows.RtlGetNtVersionNumbers()
	return fmt.Sprintf("%d.%d.%d", major, minor, build)
}

func systemUptime() time.Duration {
	milliseconds, _, _ := procGetTickCount64.Call()
	return time.Duration(milliseconds) * time.Millisecond
}

func fileTimeValue(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func systemTimes() (idle, total uint64, err error) {
	var idleTime, kernelTime, userTime windows.Filetime
	result, _, callErr := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idleTime)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime)))
	if result == 0 {
		return 0, 0, callErr
	}
	// Kernel time includes idle time.
	return fileTimeValue(idleTime), fileTimeValue(kernelTime) + fileTimeValue(userTime), nil
}

func cpuUsage(ctx context.Context, sample time.Duration) (float64, error) {
	idle1, total1, err := systemTimes()
	if err != nil {
		return 0, err
	}
	if !sleepContext(ctx, sample) {
		return 0, ctx.Err()
	}
	idle2, total2, err := systemTimes()
	if err != nil {
		return 0, err
	}
	if total2 <= total1 {
		return 0, nil
	}
	return 100 * (1 - float64(idle2-idle1)/float64(total2-total1)), nil
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memoryUsage() (map[string]any, error) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if result, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); result == 0 {
		return nil, err
	}
	used := status.TotalPhys - status.AvailPhys
	return map[string]any{"totalBytes": status.TotalPhys, "availableBytes": status.AvailPhys, "usedBytes": used,
		"usagePercent": percent(used, status.TotalPhys)}, nil
}

func diskUsage() ([]map[string]any, error) {
	buffer := make([]uint16, 512)
	length, err := windows.GetLogicalDriveStrings(uint32(len(buffer)), &buffer[0])
	if err != nil {
		return nil, err
	}
	disks := []map[string]any{}
	for _, root := range strings.Split(windows.UTF16ToString(buffer[:length]), "\x00") {
		if root == "" {
			continue
		}
		rootPointer, err := windows.UTF16PtrFromString(root)
		if err != nil || windows.GetDriveType(rootPointer) != windows.DRIVE_FIXED {
			continue
		}
		var available, total, free uint64
		if windows.GetDiskFreeSpaceEx(rootPointer, &available, &total, &free) != nil || total == 0 {
			continue
		}
		used := total - free
		disks = append(disks, map[string]any{"mountPoint": root, "totalBytes": total, "freeBytes": available,
			"usedBytes": used, "usagePercent": percent(used, total)})
	}
	return disks, nil
}

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func workingSet(pid uint32) (uint64, bool) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	counters := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	if result, _, _ := procGetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size)); result == 0 {
		return 0, false
	}
	return uint64(counters.WorkingSetSize), true
}

func listProcesses(ctx context.Context) ([]map[string]any, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	processes := []map[string]any{}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		process := map[string]any{"pid": entry.ProcessID, "parentPid": entry.ParentProcessID,
			"name": windows.UTF16ToString(entry.ExeFile[:]), "threads": entry.Threads}
		// Protected processes refuse even limited queries; their memory stays unknown.
		if memory, ok := workingSet(entry.ProcessID); ok {
			process["memoryBytes"] = memory
		}
		processes = append(processes, process)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return processes, nil
}

var serviceStates = map[uint32]string{
	windows.SERVICE_STOPPED: "stopped", windows.SERVICE_START_PENDING: "start_pending", windows.SERVICE_STOP_PENDING: "stop_pending",
	windows.SERVICE_RUNNING: "running", windows.SERVICE_CONTINUE_PENDING: "continue_pending",
	windows.SERVICE_PAUSE_PENDING: "pause_pending", windows.SERVICE_PAUSED: "paused",
}

// listServices enumerates services with SC_MANAGER_ENUMERATE_SERVICE only, which
// standard users hold; it never opens individual services for control.
func listServices(_ context.Context) (string, []map[string]any, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return "", nil, fmt.Errorf("open service control manager: %w", err)
	}
	defer windows.CloseServiceHandle(manager) //nolint:errcheck
	var needed, returned, resume uint32
	buffer := make([]byte, 64<<10)
	services := []map[string]any{}
	for {
		err := windows.EnumServicesStatusEx(manager, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL,
			&buffer[0], uint32(len(buffer)), &needed, &returned, &resume, nil)
		if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
			return "", nil, fmt.Errorf("enumerate services: %w", err)
		}
		if returned > 0 {
			entries := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buffer[0])), returned)
			for _, entry := range entries {
				state := serviceStates[entry.ServiceStatusProcess.CurrentState]
				if state == "" {
					state = "unknown"
				}
				service := map[string]any{"name": windows.UTF16PtrToString(entry.ServiceName),
					"displayName": windows.UTF16PtrToString(entry.DisplayName), "state": state}
				if pid := entry.ServiceStatusProcess.ProcessId; pid != 0 {
					service["pid"] = pid
				}
				services = append(services, service)
			}
		}
		if err == nil {
			break
		}
		if int(needed) > len(buffer) {
			buffer = make([]byte, needed)
		}
	}
	return "windows-scm", services, nil
}

func listSoftware(ctx context.Context) (string, []map[string]any, error) {
	type view struct {
		root   registry.Key
		access uint32
		scope  string
	}
	path := `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	seen := map[string]bool{}
	packages := []map[string]any{}
	for _, source := range []view{
		{registry.LOCAL_MACHINE, registry.WOW64_64KEY, "machine"},
		{registry.LOCAL_MACHINE, registry.WOW64_32KEY, "machine"},
		{registry.CURRENT_USER, 0, "user"},
	} {
		key, err := registry.OpenKey(source.root, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|source.access)
		if err != nil {
			continue
		}
		names, _ := key.ReadSubKeyNames(-1)
		for _, name := range names {
			if ctx.Err() != nil {
				key.Close()
				return "", nil, ctx.Err()
			}
			entry, err := registry.OpenKey(key, name, registry.QUERY_VALUE|source.access)
			if err != nil {
				continue
			}
			displayName, _, _ := entry.GetStringValue("DisplayName")
			versionValue, _, _ := entry.GetStringValue("DisplayVersion")
			publisher, _, _ := entry.GetStringValue("Publisher")
			installDate, _, _ := entry.GetStringValue("InstallDate")
			systemComponent, _, _ := entry.GetIntegerValue("SystemComponent")
			parent, _, _ := entry.GetStringValue("ParentKeyName")
			entry.Close()
			displayName = strings.TrimSpace(displayName)
			// Hidden system components and update entries are not user-visible software.
			if displayName == "" || systemComponent == 1 || parent != "" || seen[displayName+"\x00"+versionValue] {
				continue
			}
			seen[displayName+"\x00"+versionValue] = true
			item := map[string]any{"name": displayName, "version": versionValue, "scope": source.scope}
			if publisher != "" {
				item["publisher"] = publisher
			}
			if len(installDate) == 8 {
				item["installDate"] = installDate[:4] + "-" + installDate[4:6] + "-" + installDate[6:]
			}
			packages = append(packages, item)
		}
		key.Close()
	}
	return "windows-registry", packages, nil
}
