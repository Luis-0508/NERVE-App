package main

import (
	"context"
	"strings"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// System-management commands (#92) run only inside the privileged helper.
// Core validated the parameters before signing; the helper validates them
// again and enforces its platform's protection lists, which no approval can
// override.

// shutdownDelay lets the agent deliver the result before the host goes down.
const shutdownDelay = 15 * time.Second

// systemCommandHandlers is the helper's allowlist.
func systemCommandHandlers() map[string]collector {
	service := func(action string) collector {
		return func(ctx context.Context, parameters map[string]any) (map[string]any, error) {
			normalized, err := agentproto.ServiceParameters(parameters)
			if err != nil {
				return nil, &helperError{"invalid_parameters", err.Error()}
			}
			return controlManagedService(ctx, normalized["name"].(string), action)
		}
	}
	power := func(reboot bool) collector {
		return func(ctx context.Context, parameters map[string]any) (map[string]any, error) {
			if _, err := agentproto.NoParameters(parameters); err != nil {
				return nil, &helperError{"invalid_parameters", err.Error()}
			}
			return scheduleShutdown(ctx, reboot)
		}
	}
	return map[string]collector{
		agentproto.CapabilityServiceRestart: service("restart"),
		agentproto.CapabilityServiceStop:    service("stop"),
		agentproto.CapabilityProcessTerminate: func(ctx context.Context, parameters map[string]any) (map[string]any, error) {
			normalized, err := agentproto.ProcessParameters(parameters)
			if err != nil {
				return nil, &helperError{"invalid_parameters", err.Error()}
			}
			return terminateProcess(ctx, uint32(normalized["pid"].(int)), normalized["name"].(string))
		},
		agentproto.CapabilitySystemReboot:   power(true),
		agentproto.CapabilitySystemShutdown: power(false),
	}
}

// protectedName reports whether name is on a protection list (case-insensitive).
func protectedName(list []string, name string) bool {
	for _, item := range list {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}

func refusedService(name string) error {
	return &helperError{"protected_target", "The service " + name + " is protected and cannot be controlled remotely."}
}

func refusedProcess(name string) error {
	return &helperError{"protected_target", "The process " + name + " is protected and cannot be ended remotely."}
}

func processNameMismatch(expected, actual string) error {
	return &helperError{"process_mismatch", "The process is now " + actual + ", not " + expected + "; it was not ended."}
}
