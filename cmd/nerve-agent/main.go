package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/discovery"
)

var version = "dev"

type agent struct {
	apiURL            string
	token             string
	id                string
	hostname          string
	client            *http.Client
	logger            *slog.Logger
	heartbeatInterval time.Duration
	claimInterval     time.Duration
	executeFunc       func(context.Context, discovery.Job) error
	stateMu           sync.RWMutex
	lastError         string
}

type probeResult struct {
	observation discovery.Observation
	errors      []discovery.ScanError
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	hostname, _ := os.Hostname()
	apiURL := strings.TrimRight(envOr("NERVE_AGENT_API_URL", "http://localhost:3000/api/v1"), "/")
	token := strings.TrimSpace(os.Getenv("NERVE_DISCOVERY_AGENT_TOKEN"))
	if token == "" || token == "change-me" {
		logger.Error("NERVE_DISCOVERY_AGENT_TOKEN is not configured")
		os.Exit(1)
	}
	a := &agent{
		apiURL: apiURL, token: token, id: hostname + "-" + runtime.GOOS,
		hostname: hostname, client: &http.Client{Timeout: 20 * time.Second}, logger: logger,
		heartbeatInterval: 10 * time.Second, claimInterval: 2 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("NERVE discovery agent started", "id", a.id, "api", apiURL, "version", version)
	if err := a.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("discovery agent stopped", "error", err)
		os.Exit(1)
	}
}

func (a *agent) run(ctx context.Context) error {
	if err := a.heartbeat(ctx, a.currentError()); err != nil {
		a.logger.Warn("initial discovery heartbeat failed", "error", err)
	}

	heartbeatCtx, stopHeartbeats := context.WithCancel(ctx)
	var heartbeatWorker sync.WaitGroup
	heartbeatWorker.Add(1)
	go func() {
		defer heartbeatWorker.Done()
		a.runHeartbeats(heartbeatCtx)
	}()
	defer func() {
		stopHeartbeats()
		heartbeatWorker.Wait()
	}()

	for {
		job, err := a.claim(ctx)
		switch {
		case err == nil:
			execute := a.execute
			if a.executeFunc != nil {
				execute = a.executeFunc
			}
			if scanErr := execute(ctx, job); scanErr != nil {
				a.setError(scanErr.Error())
				a.logger.Error("discovery scan failed", "scan_id", job.ID, "error", scanErr)
			} else {
				a.setError("")
			}
		case !errors.Is(err, discovery.ErrNoJob):
			a.setError(err.Error())
			a.logger.Warn("claim discovery job failed", "error", err)
		}

		claimInterval := a.claimInterval
		if claimInterval <= 0 {
			claimInterval = 2 * time.Second
		}
		timer := time.NewTimer(claimInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *agent) runHeartbeats(ctx context.Context) {
	interval := a.heartbeatInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.heartbeat(ctx, a.currentError()); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("discovery heartbeat failed", "error", err)
			}
		}
	}
}

func (a *agent) currentError() string {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.lastError
}

func (a *agent) setError(value string) {
	a.stateMu.Lock()
	a.lastError = value
	a.stateMu.Unlock()
}

func (a *agent) heartbeat(ctx context.Context, lastError string) error {
	return a.post(ctx, "/discovery/agent/heartbeat", discovery.AgentHeartbeat{
		ID: a.id, Hostname: a.hostname, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Version: version, Capabilities: []string{"icmp", "tcp-connect", "arp-ndp-cache", "reverse-dns"}, LastError: lastError,
	}, nil)
}

func (a *agent) claim(ctx context.Context) (discovery.Job, error) {
	var job discovery.Job
	err := a.post(ctx, "/discovery/agent/jobs/claim", struct{}{}, &job)
	return job, err
}

func (a *agent) execute(ctx context.Context, job discovery.Job) error {
	a.logger.Info("discovery scan started", "scan_id", job.ID, "cidr", job.CIDR)
	observations, scanErrors, total, err := scan(ctx, job.CIDR)
	if err != nil {
		_ = a.post(context.Background(), "/discovery/agent/jobs/"+job.ID+"/complete", discovery.Completion{Status: "failed", ErrorMessage: err.Error()}, nil)
		return err
	}
	if err := a.post(ctx, "/discovery/agent/jobs/"+job.ID+"/report", discovery.Report{Observations: observations, Errors: scanErrors}, nil); err != nil {
		_ = a.post(context.Background(), "/discovery/agent/jobs/"+job.ID+"/complete", discovery.Completion{AddressesTotal: total, Status: "failed", ErrorMessage: err.Error()}, nil)
		return err
	}
	status := "succeeded"
	if len(scanErrors) > 0 {
		status = "partial"
	}
	if err := a.post(ctx, "/discovery/agent/jobs/"+job.ID+"/complete", discovery.Completion{AddressesTotal: total, Status: status}, nil); err != nil {
		return err
	}
	a.logger.Info("discovery scan completed", "scan_id", job.ID, "addresses", total, "reachable", len(observations), "errors", len(scanErrors), "status", status)
	return nil
}

func (a *agent) post(ctx context.Context, path string, value, destination any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	request.Header.Set("X-NERVE-Agent-ID", a.id)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent && strings.HasSuffix(path, "/claim") {
		return discovery.ErrNoJob
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("API %s returned HTTP %d: %s", path, response.StatusCode, strings.TrimSpace(string(message)))
	}
	if destination != nil {
		if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
			return fmt.Errorf("decode API response: %w", err)
		}
	}
	return nil
}

func scan(ctx context.Context, cidr string) ([]discovery.Observation, []discovery.ScanError, int, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("parse scan subnet: %w", err)
	}
	addresses := enumerate(prefix)
	results := make([]probeResult, len(addresses))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := 64
	if len(addresses) < workerCount {
		workerCount = len(addresses)
	}
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = probe(ctx, addresses[index])
			}
		}()
	}
	for index := range addresses {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, nil, len(addresses), ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()

	neighbors, neighborErrors := neighborTable(ctx)
	local := localAddresses()
	observations := make([]discovery.Observation, 0)
	scanErrors := neighborErrors
	for index, result := range results {
		address := addresses[index].String()
		if mac := local[address]; mac != "" {
			result.observation.Reachable = true
			result.observation.Method = "local-interface"
			result.observation.MAC = mac
		} else if mac := neighbors[address]; mac != "" {
			result.observation.MAC = mac
			if !result.observation.Reachable {
				result.observation.Reachable = true
				result.observation.Method = "arp-ndp"
			}
		}
		if result.observation.Reachable {
			observations = append(observations, result.observation)
		}
		scanErrors = append(scanErrors, result.errors...)
	}
	resolveReverseDNS(ctx, observations, 16)
	return observations, scanErrors, len(addresses), nil
}

func resolveReverseDNS(ctx context.Context, observations []discovery.Observation, workerCount int) {
	if len(observations) == 0 {
		return
	}
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(observations) {
		workerCount = len(observations)
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				lookupCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
				names, err := net.DefaultResolver.LookupAddr(lookupCtx, observations[index].Address)
				cancel()
				if err == nil && len(names) > 0 {
					observations[index].Hostname = strings.TrimSuffix(names[0], ".")
				}
			}
		}()
	}
	for index := range observations {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func enumerate(prefix netip.Prefix) []netip.Addr {
	result := make([]netip.Addr, 0)
	for address := prefix.Addr(); address.IsValid() && prefix.Contains(address); address = address.Next() {
		if address.Is4() && prefix.Bits() <= 30 && (address == prefix.Addr() || !prefix.Contains(address.Next())) {
			continue
		}
		result = append(result, address)
		if len(result) >= 4096 {
			break
		}
	}
	return result
}

func probe(ctx context.Context, address netip.Addr) probeResult {
	item := probeResult{observation: discovery.Observation{Address: address.String()}}
	probeCtx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
	defer cancel()
	args := []string{"-c", "1", "-W", "1", address.String()}
	if runtime.GOOS == "windows" {
		args = []string{"-n", "1", "-w", "900", address.String()}
	}
	command := exec.CommandContext(probeCtx, "ping", args...)
	err := command.Run()
	if err == nil {
		item.observation.Reachable = true
		item.observation.Method = "icmp"
		return item
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) && !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		item.errors = append(item.errors, discovery.ScanError{Address: address.String(), Stage: "icmp", Message: err.Error()})
	}
	for _, port := range []string{"443", "80", "22", "445", "3389", "53", "8080"} {
		connection, dialErr := net.DialTimeout("tcp", net.JoinHostPort(address.String(), port), 220*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			item.observation.Reachable = true
			item.observation.Method = "tcp:" + port
			break
		}
	}
	return item
}

var windowsNeighborPattern = regexp.MustCompile(`(?i)^\s*([0-9a-f:.]+)\s+([0-9a-f]{2}(?:-[0-9a-f]{2}){5})\s+`)

func neighborTable(ctx context.Context) (map[string]string, []discovery.ScanError) {
	neighbors := make(map[string]string)
	var commands [][]string
	if runtime.GOOS == "windows" {
		commands = [][]string{{"arp", "-a"}, {"netsh", "interface", "ipv6", "show", "neighbors"}}
	} else {
		commands = [][]string{{"ip", "neigh", "show"}}
	}
	errorsFound := make([]discovery.ScanError, 0)
	for _, specification := range commands {
		commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		output, err := exec.CommandContext(commandCtx, specification[0], specification[1:]...).Output()
		cancel()
		if err != nil {
			errorsFound = append(errorsFound, discovery.ScanError{Stage: "neighbor-table", Message: strings.Join(specification, " ") + ": " + err.Error()})
			continue
		}
		for _, line := range strings.Split(string(output), "\n") {
			if runtime.GOOS == "windows" {
				match := windowsNeighborPattern.FindStringSubmatch(line)
				if len(match) == 3 {
					if address, err := netip.ParseAddr(match[1]); err == nil {
						neighbors[address.String()] = strings.ToLower(strings.ReplaceAll(match[2], "-", ":"))
					}
				}
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			for index := 1; index+1 < len(fields); index++ {
				if fields[index] == "lladdr" {
					if address, err := netip.ParseAddr(fields[0]); err == nil {
						neighbors[address.String()] = strings.ToLower(fields[index+1])
					}
				}
			}
		}
	}
	return neighbors, errorsFound
}

func localAddresses() map[string]string {
	result := make(map[string]string)
	interfaces, _ := net.Interfaces()
	for _, item := range interfaces {
		mac := strings.ToLower(item.HardwareAddr.String())
		addresses, _ := item.Addrs()
		for _, raw := range addresses {
			address, _, err := net.ParseCIDR(raw.String())
			if err == nil {
				result[address.String()] = mac
			}
		}
	}
	return result
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
