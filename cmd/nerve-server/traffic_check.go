package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/integrations/ntopng"
	"github.com/nerve-labs/nerve/internal/traffic"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"
)

// Runs in the same container/network/configuration as the API, without a DB or a shell.
// Only diagnostic counts are printed: never passwords, URLs, or traffic IPs.
func checkTraffic(cfg config.Config) int {
	if cfg.TrafficCaptureMode == "windows-native" {
		_, port, err := net.SplitHostPort(cfg.SensorAddr)
		if err != nil {
			return 1
		}
		client := http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + port + "/health")
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "capture_unavailable"})
			return 1
		}
		defer response.Body.Close()
		_, _ = io.Copy(os.Stdout, io.LimitReader(response.Body, 8192))
		if response.StatusCode != 200 {
			return 1
		}
		return 0
	}
	var cidrs []netip.Prefix
	for _, raw := range strings.Split(cfg.TrafficLocalCIDRs, ",") {
		if p, e := netip.ParsePrefix(strings.TrimSpace(raw)); e == nil {
			cidrs = append(cidrs, p)
		}
	}
	source := ntopng.New(ntopng.Config{URL: cfg.NtopngURL, Username: cfg.NtopngUsername, Password: cfg.NtopngPassword, InterfaceID: cfg.NtopngInterfaceID, Timeout: cfg.NtopngTimeout, LocalCIDRs: cidrs})
	snapshot, err := source.Live(context.Background())
	status, code := "connected", 0
	if err != nil {
		code = 1
		switch {
		case errors.Is(err, ntopng.ErrNotConfigured):
			status = "not_configured"
		case errors.Is(err, ntopng.ErrAuthentication):
			status = "authentication_failed"
		case errors.Is(err, ntopng.ErrInvalidConfiguration):
			status = "invalid_configuration"
		case errors.Is(err, ntopng.ErrInvalidResponse):
			status = "invalid_response"
		default:
			status = "unreachable"
		}
	} else if snapshot.Summary.ActiveFlows == 0 {
		status = "no_active_external_flows"
	}
	capture := traffic.CaptureInfo(cfg.TrafficCaptureMode, cfg.NtopngURL, cfg.NtopngInterfaceID)
	if snapshot.Capture != nil {
		capture.InterfaceID = snapshot.Capture.InterfaceID
		capture.InterfaceName = snapshot.Capture.InterfaceName
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": status, "capture": capture, "externalFlows": snapshot.Summary.ActiveFlows, "unlocatedFlows": snapshot.Summary.UnlocatedFlows, "truncated": snapshot.Truncated})
	return code
}
