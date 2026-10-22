//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// runningAsService reports whether the Service Control Manager started us.
func runningAsService() bool {
	inService, err := svc.IsWindowsService()
	return err == nil && inService
}

func detectRunMode() string {
	if runningAsService() {
		return "windows_service"
	}
	return "foreground"
}

// windowsService adapts the agent to the Service Control Manager.
type windowsService struct {
	options runOptions
	events  *eventlog.Log
}

// runAsService blocks until the SCM stops the service.
func runAsService(options runOptions) int {
	events, _ := eventlog.Open(serviceName)
	if events != nil {
		defer events.Close()
	}
	handler := &windowsService{options: options, events: events}
	if err := svc.Run(serviceName, handler); err != nil {
		if events != nil {
			_ = events.Error(1, "service failed: "+err.Error())
		}
		return 1
	}
	return 0
}

func (s *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 10000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- runAgent(ctx, s.options) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	s.info("NERVE agent service started (version " + version + ")")
	for {
		select {
		case code := <-done:
			// The agent ended on its own (for example: not enrolled). A
			// service-specific exit code lets the recovery actions restart it.
			s.error(fmt.Sprintf("NERVE agent stopped unexpectedly (exit code %d); see %s", code, filepath.Join(s.options.stateDir, "logs", logFileName)))
			if code == 0 {
				code = 1
			}
			return true, uint32(code)
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: uint32((stopGracePeriod + 5*time.Second).Milliseconds())}
				cancel()
				select {
				case <-done:
				case <-time.After(stopGracePeriod + 2*time.Second):
				}
				s.info("NERVE agent service stopped")
				return false, 0
			}
		}
	}
}

func (s *windowsService) info(message string) {
	if s.events != nil {
		_ = s.events.Info(1, message)
	}
}

func (s *windowsService) error(message string) {
	if s.events != nil {
		_ = s.events.Error(1, message)
	}
}

// sdNotify is a no-op outside systemd.
func sdNotify(string) {}

func watchdogInterval() time.Duration { return 0 }

func requireAdministrator() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("run this command from an elevated (Administrator) prompt")
	}
	return nil
}

func installService(options installOptions) error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" && !strings.HasPrefix(strings.ToLower(executable), strings.ToLower(programFiles)+`\`) {
		fmt.Printf("Warning: %s is outside %s. A service binary must not be writable by non-administrators; the installer script copies it to %s\\NERVE\\Agent.\n", executable, programFiles, programFiles)
	}
	// Only the machine directory keeps the service account in its DACL when an
	// administrator later re-enrolls (see securePrivateDir).
	if !sameDir(options.StateDir, machineStateDir()) {
		return fmt.Errorf("the Windows service uses %s; --state-dir cannot be changed", machineStateDir())
	}
	options.StateDir = machineStateDir()
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer manager.Disconnect()

	config := mgr.Config{
		DisplayName:      serviceDisplayName,
		Description:      serviceDescription,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ServiceStartName: serviceAccount,
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
		ErrorControl:     mgr.ErrorNormal,
	}
	args := []string{"run", "--state-dir", options.StateDir}
	service, err := manager.OpenService(serviceName)
	if err == nil {
		fmt.Println("Service already installed; updating its configuration.")
		current, err := service.Config()
		if err != nil {
			service.Close()
			return err
		}
		config.ServiceType = current.ServiceType
		config.BinaryPathName = commandLine(executable, args)
		if err := service.UpdateConfig(config); err != nil {
			service.Close()
			return fmt.Errorf("update service: %w", err)
		}
	} else {
		service, err = manager.CreateService(serviceName, executable, config, args...)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		fmt.Println("Service", serviceName, "created (account", serviceAccount+").")
	}
	defer service.Close()
	// Restart after crashes and after non-zero exits: 5 s, 10 s, then every
	// 60 s; the failure counter resets after a day without failures.
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, uint32((24 * time.Hour).Seconds())); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil && !strings.Contains(err.Error(), "exists") {
		fmt.Println("Warning: could not register the event log source:", err)
	}

	if err := os.MkdirAll(options.StateDir, 0o700); err != nil {
		return err
	}
	sid, err := serviceAccountSID()
	if err != nil {
		return fmt.Errorf("resolve %s: %w", serviceAccount, err)
	}
	if err := restrictDir(options.StateDir, sid); err != nil {
		return fmt.Errorf("restrict %s: %w", options.StateDir, err)
	}
	id, err := prepareServiceState(options)
	if err != nil {
		return err
	}
	if id == nil {
		fmt.Println("The service is installed but not enrolled. Enroll with: nerve-agent service install --token-file <file>, then it starts automatically.")
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

func commandLine(executable string, args []string) string {
	parts := []string{syscall.EscapeArg(executable)}
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

func uninstallService(stateDir string, purge bool) error {
	if err := requireAdministrator(); err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(serviceName)
	if err != nil {
		fmt.Println("Service is not installed.")
	} else {
		_, _ = stopService(service)
		err := service.Delete()
		service.Close()
		if err != nil {
			return fmt.Errorf("delete service: %w", err)
		}
		fmt.Println("Service", serviceName, "removed.")
	}
	_ = eventlog.Remove(serviceName)
	if purge {
		if err := os.RemoveAll(stateDir); err != nil {
			return fmt.Errorf("remove %s: %w", stateDir, err)
		}
		fmt.Println("Removed", stateDir+". Revoke or remove this agent in NERVE.")
	} else {
		fmt.Println("The identity in", stateDir, "was kept; reinstalling reuses it. Use --purge to delete it.")
	}
	return nil
}

func controlService(action string) (string, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(serviceName)
	if err != nil {
		return "not installed", nil
	}
	defer service.Close()
	switch action {
	case "stop":
		return stopService(service)
	case "start", "restart":
		if action == "restart" {
			if _, err := stopService(service); err != nil {
				return "", err
			}
		}
		if err := service.Start(); err != nil {
			return "", fmt.Errorf("start service: %w", err)
		}
		return waitForState(service, svc.Running)
	default:
		status, err := service.Query()
		if err != nil {
			return "", err
		}
		return stateName(status.State), nil
	}
}

func stopService(service *mgr.Service) (string, error) {
	status, err := service.Query()
	if err != nil {
		return "", err
	}
	if status.State == svc.Stopped {
		return "stopped", nil
	}
	if _, err := service.Control(svc.Stop); err != nil {
		return "", fmt.Errorf("stop service: %w", err)
	}
	return waitForState(service, svc.Stopped)
}

func waitForState(service *mgr.Service, want svc.State) (string, error) {
	deadline := time.Now().Add(stopGracePeriod + 15*time.Second)
	for {
		status, err := service.Query()
		if err != nil {
			return "", err
		}
		if status.State == want {
			return stateName(want), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("service is %s, expected %s", stateName(status.State), stateName(want))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	default:
		return fmt.Sprintf("state %d", state)
	}
}
