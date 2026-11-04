//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func currentHelperTestSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func testPipeListener(t *testing.T) net.Listener {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\NERVEAgentHelper-test-%d-%x`, os.Getpid(), nonce)
	listener, err := listenWindowsHelper(name, currentHelperTestSID(t), currentHelperTestSID(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func windowsHelper(t *testing.T, server *helperServer) string {
	t.Helper()
	listener := testPipeListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("helper serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("helper did not stop")
		}
	})
	return listener.Addr().String()
}

func TestWindowsHelperPeerChecksAndReplay(t *testing.T) {
	sid := currentHelperTestSID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	server.authorized = peerSIDIn(sid)
	pipe := windowsHelper(t, server)
	client := &helperClient{dial: dialWindowsHelper(pipe, sid), agentID: "agent-1"}
	command := approvedRebootCommand("windows-1", testCoreKey, time.Now())
	if _, err := client.run(ctx, command); err != nil {
		t.Fatalf("authorized caller: %v", err)
	}
	_, err := client.run(ctx, command)
	var refusal *helperError
	if !errors.As(err, &refusal) || refusal.Code != "replayed" || *runs != 1 {
		t.Fatalf("replay: err=%v runs=%d", err, *runs)
	}
	if c, err := dialWindowsHelper(pipe, "S-1-5-7")(ctx); err == nil {
		c.Close()
		t.Fatal("accepted a pipe owned by an unexpected account")
	}
}

func TestWindowsHelperRefusesUnauthorizedCaller(t *testing.T) {
	server, runs := testHelperServer(t, filepath.Join(t.TempDir(), helperReplayFileName))
	server.authorized = peerSIDIn("S-1-5-7")
	client := &helperClient{dial: dialWindowsHelper(windowsHelper(t, server), currentHelperTestSID(t)), agentID: "agent-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.run(ctx, approvedRebootCommand("windows-2", testCoreKey, time.Now()))
	var refusal *helperError
	if !errors.As(err, &refusal) || refusal.Code != "helper_unavailable" || *runs != 0 {
		t.Fatalf("unauthorized caller: err=%v runs=%d", err, *runs)
	}
}

func TestWindowsHelperListenerCloseUnblocksAccept(t *testing.T) {
	listener := testPipeListener(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := listener.Accept()
		done <- err
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- listener.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Accept succeeded after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Accept")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("listener Close hung")
	}
}

func connectedTestPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener := testPipeListener(t)
	accepted := make(chan net.Conn, 1)
	acceptError := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			acceptError <- err
			return
		}
		accepted <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialWindowsHelper(listener.Addr().String(), currentHelperTestSID(t))(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	select {
	case server := <-accepted:
		t.Cleanup(func() { server.Close() })
		return client, server
	case err := <-acceptError:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("Accept timed out")
	}
	return nil, nil
}

func TestWindowsPipeCloseUnblocksRead(t *testing.T) {
	client, _ := connectedTestPipe(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := client.Read(make([]byte, 1))
		done <- err
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read succeeded without data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Read")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("connection Close hung")
	}
}

func TestWindowsPipeChangedDeadlineUnblocksRead(t *testing.T) {
	client, _ := connectedTestPipe(t)
	done := make(chan error, 1)
	go func() {
		_, err := client.Read(make([]byte, 1))
		done <- err
	}()
	if err := client.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not unblock Read")
	}
	if err := client.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsHelperStateMustBeAdministratorOnly(t *testing.T) {
	// A directory owned by a standard account is never trusted, even with a
	// protected DACL: the account could change the DACL again.
	dir := t.TempDir()
	if err := restrictDir(dir); err != nil {
		t.Fatal(err)
	}
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("an elevated test run owns files as Administrators")
	}
	if err := checkHelperSecurity(dir); err == nil {
		t.Fatal("accepted helper state owned by a standard account")
	}
	if _, err := loadHelperPin(dir); err == nil {
		t.Fatal("loaded a pin from untrusted state")
	}
}

func TestWindowsHelperPipeClientCannotCreateInstances(t *testing.T) {
	const fileCreatePipeInstance = 0x4 // FILE_APPEND_DATA on a pipe
	if helperPipeClientAccess&fileCreatePipeInstance != 0 || helperPipeClientAccess&windows.GENERIC_WRITE != 0 {
		t.Fatalf("client access %#x allows creating pipe instances", helperPipeClientAccess)
	}
}

func TestProcessServicesListsHostedServices(t *testing.T) {
	// The test process hosts no service; enumerating the SCM must still work
	// for a standard user.
	services, err := processServices(uint32(os.Getpid()))
	if err != nil || len(services) != 0 {
		t.Fatalf("services=%v err=%v", services, err)
	}
	if !serviceProtected("TermService", "stop") || serviceProtected("TermService", "restart") || !serviceProtected("nerveagent", "restart") {
		t.Fatal("protection lists wrong")
	}
}

func TestReadLogsFromEventLogs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entries, err := readLogs(ctx, "", 5, 7*24*time.Hour)
	if err != nil {
		t.Skipf("event logs not readable here: %v", err)
	}
	if len(entries) > 10 {
		t.Fatalf("read %d entries for 5 lines from two logs", len(entries))
	}
	// A provider name with spaces stays one literal in the query.
	if _, err := readLogs(ctx, "Service Control Manager", 3, 7*24*time.Hour); err != nil {
		t.Fatalf("provider filter: %v", err)
	}
}
