package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// Collectors are read-only by construction: they read kernel/OS state or run a
// fixed query program with fixed arguments. No operator-supplied text is ever
// executed. Process command lines and environment variables are deliberately
// not collected because they frequently contain secrets.

var errUnsupported = errors.New("capability is not supported on this host")

type collector func(ctx context.Context, parameters map[string]any) (map[string]any, error)

func collectorFor(name string) (collector, bool) {
	switch name {
	case agentproto.CapabilitySystemInfo:
		return collectSystemInfo, true
	case agentproto.CapabilitySystemMetrics:
		return collectSystemMetrics, true
	case agentproto.CapabilityProcessList:
		return collectProcessList, true
	case agentproto.CapabilityServiceList:
		return collectServiceList, true
	case agentproto.CapabilitySoftwareList:
		return collectSoftwareList, true
	}
	return nil, false
}

// supportedCapabilities lists what this agent can actually do on this host.
// NERVE_AGENT_DISABLED_CAPABILITIES lets the host owner switch any off.
func supportedCapabilities() []string {
	disabled := map[string]bool{}
	for _, name := range splitList(os.Getenv("NERVE_AGENT_DISABLED_CAPABILITIES")) {
		disabled[name] = true
	}
	result := []string{}
	for _, name := range platformCapabilities() {
		if !disabled[name] {
			result = append(result, name)
		}
	}
	return result
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func collectSystemInfo(_ context.Context, _ map[string]any) (map[string]any, error) {
	hostname, _ := os.Hostname()
	uptime := systemUptime()
	result := map[string]any{
		"hostname": hostname, "os": runtime.GOOS, "osVersion": osVersion(), "kernelVersion": kernelVersion(),
		"architecture": runtime.GOARCH, "logicalCpus": runtime.NumCPU(), "agentVersion": version,
		"collectedAt": time.Now().UTC(),
	}
	if uptime > 0 {
		result["uptimeSeconds"] = int64(uptime.Seconds())
		result["bootTime"] = time.Now().Add(-uptime).UTC().Truncate(time.Second)
	}
	return result, nil
}

func collectSystemMetrics(ctx context.Context, _ map[string]any) (map[string]any, error) {
	cpu, err := cpuUsage(ctx, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("cpu: %w", err)
	}
	memory, err := memoryUsage()
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	disks, err := diskUsage()
	if err != nil {
		return nil, fmt.Errorf("disks: %w", err)
	}
	return map[string]any{
		"collectedAt": time.Now().UTC(),
		"cpu":         map[string]any{"usagePercent": round1(cpu), "logicalCores": runtime.NumCPU(), "sampleMilliseconds": 500},
		"memory":      memory,
		"disks":       disks,
	}, nil
}

func collectProcessList(ctx context.Context, parameters map[string]any) (map[string]any, error) {
	limit := 500
	if raw, ok := parameters["limit"].(float64); ok && raw >= 1 && raw <= 1000 {
		limit = int(raw)
	}
	processes, err := listProcesses(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(processes, func(i, j int) bool {
		left, _ := processes[i]["memoryBytes"].(uint64)
		right, _ := processes[j]["memoryBytes"].(uint64)
		if left != right {
			return left > right
		}
		return processes[i]["pid"].(uint32) < processes[j]["pid"].(uint32)
	})
	return listResult("", processes, limit), nil
}

func collectServiceList(ctx context.Context, _ map[string]any) (map[string]any, error) {
	source, services, err := listServices(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(services, func(i, j int) bool { return fmt.Sprint(services[i]["name"]) < fmt.Sprint(services[j]["name"]) })
	return listResult(source, services, 5000), nil
}

func collectSoftwareList(ctx context.Context, _ map[string]any) (map[string]any, error) {
	source, packages, err := listSoftware(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(packages, func(i, j int) bool { return fmt.Sprint(packages[i]["name"]) < fmt.Sprint(packages[j]["name"]) })
	return listResult(source, packages, 10000), nil
}

func listResult(source string, items []map[string]any, limit int) map[string]any {
	result := map[string]any{"collectedAt": time.Now().UTC(), "total": len(items)}
	if source != "" {
		result["source"] = source
	}
	if len(items) > limit {
		items = items[:limit]
		result["truncated"] = true
	}
	result["items"] = items
	result["returned"] = len(items)
	return result
}

// encodeOutput marshals a collector result within maxBytes. List results are
// shortened (and marked truncated) rather than failing outright.
func encodeOutput(value map[string]any, maxBytes int) ([]byte, bool, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	if len(encoded) <= maxBytes {
		return encoded, false, nil
	}
	items, ok := value["items"].([]map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("output of %d bytes exceeds the %d byte limit", len(encoded), maxBytes)
	}
	low, high := 0, len(items)
	var best []byte
	for low <= high {
		middle := (low + high) / 2
		candidate := make(map[string]any, len(value))
		for key, item := range value {
			candidate[key] = item
		}
		candidate["items"] = items[:middle]
		candidate["returned"] = middle
		candidate["truncated"] = true
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return nil, false, err
		}
		if len(encoded) <= maxBytes {
			best = encoded
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best == nil {
		return nil, false, fmt.Errorf("output exceeds the %d byte limit", maxBytes)
	}
	return best, true, nil
}

func round1(value float64) float64 {
	return float64(int64(value*10+0.5)) / 10
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round1(float64(used) * 100 / float64(total))
}
