//go:build !linux && !windows

package main

import (
	"context"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// Other platforms only report basic system information until dedicated
// collectors exist.
func platformCapabilities() []string { return []string{agentproto.CapabilitySystemInfo} }

func kernelVersion() string { return "" }

func systemUptime() time.Duration { return 0 }

func cpuUsage(context.Context, time.Duration) (float64, error) { return 0, errUnsupported }

func memoryUsage() (map[string]any, error) { return nil, errUnsupported }

func diskUsage() ([]map[string]any, error) { return nil, errUnsupported }

func listProcesses(context.Context) ([]map[string]any, error) { return nil, errUnsupported }

func listServices(context.Context) (string, []map[string]any, error) { return "", nil, errUnsupported }

func listSoftware(context.Context) (string, []map[string]any, error) { return "", nil, errUnsupported }
