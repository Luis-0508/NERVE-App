//go:build linux

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSystemdUnitIsUnprivilegedAndRestarts(t *testing.T) {
	unit := systemdUnit("/opt/nerve agent/nerve-agent", "/var/lib/nerve-agent")
	for _, want := range []string{
		`ExecStart="/opt/nerve agent/nerve-agent" run --state-dir /var/lib/nerve-agent`,
		"User=nerve-agent", "Restart=always", "StartLimitIntervalSec=0", "Type=notify", "WatchdogSec=",
		"NoNewPrivileges=true", "CapabilityBoundingSet=\n", "ProtectSystem=strict", "UMask=0077",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "User=root") {
		t.Fatal("unit runs as root")
	}
}

func TestWatchdogInterval(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "180000000")
	t.Setenv("WATCHDOG_PID", "")
	if got := watchdogInterval(); got != 90*time.Second {
		t.Fatalf("watchdog interval %s", got)
	}
	t.Setenv("WATCHDOG_PID", "1")
	if watchdogInterval() != 0 {
		t.Fatal("watchdog for another process was honoured")
	}
}
