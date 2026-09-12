package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/integrations/ntopng"
	"github.com/nerve-labs/nerve/internal/traffic"
	"net/netip"
	"os"
	"strings"
)

// Runs in the same container/network/configuration as the API, without a DB or a shell.
// Only diagnostic counts are printed: never passwords, URLs, or traffic IPs.
func checkTraffic(cfg config.Config) int {
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
