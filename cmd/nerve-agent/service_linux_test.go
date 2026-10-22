//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestChownTreeHandsOverExistingStateWithoutFollowingLinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file owners")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	for _, path := range []string{filepath.Join(dir, identityFileName), filepath.Join(dir, "logs", logFileName), outside} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	const uid, gid = 4242, 4242
	if err := chownTree(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(dir, identityFileName), filepath.Join(dir, "logs"), filepath.Join(dir, "logs", logFileName), filepath.Join(dir, "link")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat := info.Sys().(*syscall.Stat_t); stat.Uid != uid || stat.Gid != gid {
			t.Fatalf("%s still owned by %d:%d", path, stat.Uid, stat.Gid)
		}
	}
	info, _ := os.Stat(outside)
	if info.Sys().(*syscall.Stat_t).Uid == uid {
		t.Fatal("symlink target outside the state directory was changed")
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
