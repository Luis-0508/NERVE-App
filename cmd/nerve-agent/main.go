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
	scanCtx, cancel := context.WithTimeout(ctx, discoveryScanTimeout(job.CIDR))
	defer cancel()

	reportPath := "/discovery/agent/jobs/" + job.ID + "/report"
	total, reachable, errorCount, err := scan(scanCtx, job.CIDR, func(report discovery.Report) error {
		return a.post(scanCtx, reportPath, report, nil)
	})
	if err != nil {
		completionCtx, completionCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer completionCancel()
		_ = a.post(completionCtx, "/discovery/agent/jobs/"+job.ID+"/complete", discovery.Completion{AddressesTotal: total, Status: "failed", ErrorMessage: err.Error()}, nil)
		return err
	}

	status := "succeeded"
	if errorCount > 0 {
		status = "partial"
	}
	if err := a.post(ctx, "/discovery/agent/jobs/"+job.ID+"/complete", discovery.Completion{AddressesTotal: total, Status: status}, nil); err != nil {
		return err
	}
	a.logger.Info("discovery scan completed", "scan_id", job.ID, "addresses", total, "reachable", reachable, "errors", errorCount, "status", status)
	return nil
}

func discoveryScanTimeout(cidr string) time.Duration {
	_, total, err := discovery.NormalizeCIDR(cidr)
	if err != nil || total <= 0 {
		return 2 * time.Minute
	}
	batches := (total + 63) / 64
	timeout := 60*time.Second + time.Duration(batches)*4*time.Second
	if timeout > 6*time.Minute {
		return 6 * time.Minute
	}
	return timeout
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

func scan(ctx context.Context, cidr string, report func(discovery.Report) error) (int, int, int, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse scan subnet: %w", err)
	}
	addresses := enumerate(prefix)
	total := len(addresses)
	if total == 0 {
		return 0, 0, 0, nil
	}

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	local := localAddresses()
	neighbors, neighborErrors := neighborTable(scanCtx)

	jobs := make(chan netip.Addr)
	results := make(chan probeResult, 64)
	workerCount := 64
	if total < workerCount {
		workerCount = total
	}
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for address := range jobs {
				result := probe(scanCtx, address)
				select {
				case results <- result:
				case <-scanCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, address := range addresses {
			select {
			case jobs <- address:
			case <-scanCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	reported := make(map[string]struct{}, total)
	pendingObservations := make([]discovery.Observation, 0, 24)
	pendingErrors := append([]discovery.ScanError(nil), neighborErrors...)
	reachable := 0
	errorCount := 0
	lastNeighborRefresh := time.Now()

	refreshNeighbors := func() {
		latest, _ := neighborTable(scanCtx)
		for address, mac := range latest {
			neighbors[address] = mac
		}
		lastNeighborRefresh = time.Now()
	}
	enrich := func(observations []discovery.Observation) {
		for index := range observations {
			address := observations[index].Address
			if mac := local[address]; mac != "" {
				observations[index].Reachable = true
				observations[index].Method = "local-interface"
				observations[index].MAC = mac
			} else if mac := neighbors[address]; mac != "" {
				observations[index].MAC = mac
				if !observations[index].Reachable {
					observations[index].Reachable = true
					observations[index].Method = "arp-ndp"
				}
			}
		}
		resolveReverseDNS(scanCtx, observations, 16)
	}
	flush := func(refresh bool) error {
		if len(pendingObservations) == 0 && len(pendingErrors) == 0 {
			return nil
		}
		if refresh && time.Since(lastNeighborRefresh) >= time.Second {
			refreshNeighbors()
		}
		if len(pendingObservations) > 0 {
			enrich(pendingObservations)
		}
		batch := discovery.Report{
			Observations: append([]discovery.Observation(nil), pendingObservations...),
			Errors:       append([]discovery.ScanError(nil), pendingErrors...),
		}
		if err := report(batch); err != nil {
			return err
		}
		for _, observation := range pendingObservations {
			reported[observation.Address] = struct{}{}
		}
		reachable += len(pendingObservations)
		errorCount += len(pendingErrors)
		pendingObservations = pendingObservations[:0]
		pendingErrors = pendingErrors[:0]
		return nil
	}

	flushTicker := time.NewTicker(750 * time.Millisecond)
	defer flushTicker.Stop()
	for results != nil {
		select {
		case <-scanCtx.Done():
			return total, reachable, errorCount, scanCtx.Err()
		case <-flushTicker.C:
			if err := flush(true); err != nil {
				return total, reachable, errorCount, fmt.Errorf("report live discovery batch: %w", err)
			}
		case result, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			address := result.observation.Address
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
				pendingObservations = append(pendingObservations, result.observation)
			}
			pendingErrors = append(pendingErrors, result.errors...)
			if len(pendingObservations) >= 16 || len(pendingErrors) >= 64 {
				if err := flush(true); err != nil {
					return total, reachable, errorCount, fmt.Errorf("report live discovery batch: %w", err)
				}
			}
		}
	}
	if err := flush(true); err != nil {
		return total, reachable, errorCount, fmt.Errorf("report live discovery batch: %w", err)
	}

	// A final neighbor-cache sweep preserves discovery of devices that block all
	// active probes but were still resolved at layer 2 while the scan ran.
	refreshNeighbors()
	for _, address := range addresses {
		key := address.String()
		if _, exists := reported[key]; exists {
			continue
		}
		mac := local[key]
		method := "local-interface"
		if mac == "" {
			mac = neighbors[key]
			method = "arp-ndp"
		}
		if mac == "" {
			continue
		}
		pendingObservations = append(pendingObservations, discovery.Observation{Address: key, Reachable: true, MAC: mac, Method: method})
		if len(pendingObservations) >= 16 {
			if err := flush(false); err != nil {
				return total, reachable, errorCount, fmt.Errorf("report neighbor discovery batch: %w", err)
			}
		}
	}
	if err := flush(false); err != nil {
		return total, reachable, errorCount, fmt.Errorf("report neighbor discovery batch: %w", err)
	}
	return total, reachable, errorCount, nil
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
