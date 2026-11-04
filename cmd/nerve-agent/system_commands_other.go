//go:build !linux && !windows

package main

import (
	"context"
	"errors"
	"time"
)

var errNoSystemCommands = errors.New("system-management commands are not available on this platform")

func controlManagedService(context.Context, string, string) (map[string]any, error) {
	return nil, errNoSystemCommands
}

func terminateProcess(context.Context, uint32, string) (map[string]any, error) {
	return nil, errNoSystemCommands
}

func scheduleShutdown(context.Context, bool) (map[string]any, error) { return nil, errNoSystemCommands }

func readLogs(context.Context, string, int, time.Duration) ([]logEntry, error) {
	return nil, errNoSystemCommands
}
