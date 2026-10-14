package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// check is one diagnostic result. Details never contain keys or tokens.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail, skip
	Detail string `json:"detail"`
}

type diagnosis struct {
	Version  string  `json:"version"`
	Platform string  `json:"platform"`
	StateDir string  `json:"stateDir"`
	Checks   []check `json:"checks"`
}

func (d *diagnosis) add(name, status, format string, args ...any) {
	d.Checks = append(d.Checks, check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
}

func (d *diagnosis) failed() bool {
	for _, item := range d.Checks {
		if item.Status == "fail" {
			return true
		}
	}
	return false
}

func runDiagnose(args []string) int {
	flags := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	stateDir := flags.String("state-dir", defaultStateDir(), "directory for the agent identity")
	asJSON := flags.Bool("json", false, "print the result as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result := diagnose(ctx, *stateDir)
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(result)
	} else {
		fmt.Printf("NERVE agent %s on %s\nState directory: %s\n\n", result.Version, result.Platform, result.StateDir)
		for _, item := range result.Checks {
			fmt.Printf("[%-4s] %-12s %s\n", item.Status, item.Name, item.Detail)
		}
	}
	if result.failed() {
		return 1
	}
	return 0
}

// diagnose checks each layer in order (local state, network, TLS, clock, Core
// authentication) and stops at the first layer that cannot be reached.
func diagnose(ctx context.Context, stateDir string) diagnosis {
	result := diagnosis{Version: version, Platform: runtime.GOOS + "/" + runtime.GOARCH, StateDir: stateDir}
	if state, err := controlService("status"); err == nil {
		result.add("service", "ok", "%s", state)
	} else {
		result.add("service", "skip", "%v", err)
	}

	id, err := loadIdentity(stateDir)
	switch {
	case err == nil:
		result.add("identity", "ok", "agent %s, key %s, pinned Core key %s", id.AgentID, id.Fingerprint, agentproto.CoreKeyID(id.coreKey))
	case errors.Is(err, fs.ErrNotExist):
		result.add("identity", "fail", "not enrolled (no %s); run `nerve-agent enroll` or `service install --token-file`", identityFileName)
	case errors.Is(err, fs.ErrPermission):
		result.add("identity", "fail", "permission denied; run as Administrator/root or as the service account")
	default:
		result.add("identity", "fail", "%v", err)
	}
	config, err := resolveSettings(stateDir, id)
	if err != nil {
		result.add("config", "fail", "%v", err)
		return result
	}
	if config.APIURL == "" {
		result.add("config", "fail", "no Core API URL configured")
		return result
	}
	result.add("config", "ok", "Core %s, log level %s", config.APIURL, config.LogLevel)
	transport, err := newTransport(config.CAFile)
	if err != nil {
		result.add("tls-config", "fail", "%v", err)
		return result
	}
	if config.CAFile != "" {
		result.add("tls-config", "ok", "additional CA %s", config.CAFile)
	}

	parsed, _ := url.Parse(config.APIURL)
	host, port := parsed.Hostname(), parsed.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[parsed.Scheme]
	}
	addresses, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		result.add("dns", "fail", "%s: %v", host, err)
		return result
	}
	result.add("dns", "ok", "%s → %v", host, addresses)
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		result.add("tcp", "fail", "%s:%s: %v", host, port, err)
		return result
	}
	connection.Close()
	result.add("tcp", "ok", "connected to %s:%s", host, port)

	if parsed.Scheme == "https" {
		tlsConfig := transport.TLSClientConfig.Clone()
		tlsConfig.ServerName = host
		tlsConnection, err := tls.DialWithDialer(&dialer, "tcp", net.JoinHostPort(host, port), tlsConfig)
		if err != nil {
			result.add("tls", "fail", "certificate not trusted or handshake failed: %v", err)
			return result
		}
		state := tlsConnection.ConnectionState()
		tlsConnection.Close()
		leaf := state.PeerCertificates[0]
		remaining := time.Until(leaf.NotAfter)
		status := "ok"
		if remaining < 14*24*time.Hour {
			status = "warn"
		}
		result.add("tls", status, "%s, certificate for %s issued by %s, expires %s", tls.VersionName(state.Version),
			leaf.Subject.CommonName, leaf.Issuer.CommonName, leaf.NotAfter.Format(time.DateOnly))
	} else if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		result.add("tls", "warn", "plain HTTP: requests are authenticated, but results and host facts are readable on the network")
	}

	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, config.APIURL, nil)
	before := time.Now()
	response, err := client.Do(request)
	if err != nil {
		result.add("core", "fail", "%v", err)
		return result
	}
	response.Body.Close()
	if serverTime, err := http.ParseTime(response.Header.Get("Date")); err == nil {
		skew := serverTime.Sub(before.Add(time.Since(before) / 2)).Round(time.Second)
		status := "ok"
		if skew.Abs() > agentproto.MaxClockSkew/2 {
			status = "fail"
		}
		result.add("clock", status, "offset to Core %s (limit ±%s)", skew, agentproto.MaxClockSkew)
	}
	if id == nil {
		return result
	}
	var identityResponse agentproto.IdentityResponse
	if _, err := newManagementClientWith(config.APIURL, id, transport).post(ctx, "/agent/identity", struct{}{}, &identityResponse); err != nil {
		hint := ""
		if requiresReenrollment(err) {
			hint = " (revoked or unknown key: re-enroll this agent)"
		}
		result.add("core-auth", "fail", "%v%s", err, hint)
		return result
	}
	result.add("core-auth", "ok", "Core accepts agent %s (%s); response signed by the pinned Core key", identityResponse.AgentID, identityResponse.Status)
	return result
}
