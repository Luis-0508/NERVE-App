//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unixHelper serves server on a real Unix socket and returns its path.
func unixHelper(t *testing.T, server *helperServer) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = server.serve(ctx, listener)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return socket
}

func TestHelperSocketChecksPeerCredentials(t *testing.T) {
	uid := uint32(os.Getuid())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	server.authorized = peerUIDIn(uid)
	client := &helperClient{dial: dialUnixHelper(unixHelper(t, server), uid), agentID: "agent-1"}
	if _, err := client.run(ctx, approvedRebootCommand("s1", testCoreKey, time.Now())); err != nil || *runs != 1 {
		t.Fatalf("authorized caller: err=%v runs=%d", err, *runs)
	}

	// The helper refuses a caller running as another account.
	refusing, refusedRuns := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	refusing.authorized = peerUIDIn(uid + 1)
	client = &helperClient{dial: dialUnixHelper(unixHelper(t, refusing), uid), agentID: "agent-1"}
	_, err := client.run(ctx, approvedRebootCommand("s2", testCoreKey, time.Now()))
	var refusal *helperError
	if !errors.As(err, &refusal) || refusal.Code != "helper_unavailable" || *refusedRuns != 0 {
		t.Fatalf("unauthorized caller: err=%v runs=%d", err, *refusedRuns)
	}

	// The agent refuses a socket served by an unexpected account.
	if _, err := dialUnixHelper(unixHelper(t, server), uid+1)(ctx); err == nil {
		t.Fatal("connected to a helper served by the wrong account")
	}
}

func TestHelperPinMustBeRootOwnedAndNotWritable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a root-owned pin")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeHelperPin(dir, helperPin{Version: 1, AgentID: "agent-1", CoreKeys: []string{base64.StdEncoding.EncodeToString(testCorePublic)}}); err != nil {
		t.Fatal(err)
	}
	if pin, err := loadHelperPin(dir); err != nil || pin.AgentID != "agent-1" || len(pin.keys) != 1 {
		t.Fatalf("pin=%+v err=%v", pin, err)
	}
	if err := os.Chmod(filepath.Join(dir, helperPinFileName), 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHelperPin(dir); err == nil {
		t.Fatal("accepted a group-writable pin")
	}
	if err := os.Chmod(filepath.Join(dir, helperPinFileName), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(dir, helperPinFileName), 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHelperPin(dir); err == nil {
		t.Fatal("accepted a pin owned by another account")
	}
}

func TestHelperUnitIsIsolated(t *testing.T) {
	unit := helperSystemdUnit("/usr/local/bin/nerve-agent")
	for _, line := range []string{"ExecStart=/usr/local/bin/nerve-agent helper", "User=root", "Group=" + serviceUser,
		"PrivateNetwork=true", "RestrictAddressFamilies=AF_UNIX", "CapabilityBoundingSet=\n", "NoNewPrivileges=true",
		"RuntimeDirectoryMode=0750", "StateDirectoryMode=0700"} {
		if !strings.Contains(unit, line) {
			t.Errorf("helper unit lacks %q", line)
		}
	}
}
