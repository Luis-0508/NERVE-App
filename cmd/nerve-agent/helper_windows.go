//go:build windows

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	helperServiceName = "NERVEAgentHelper"
	helperSocketPath  = `\\.\pipe\NERVEAgentHelper`
	systemSID         = "S-1-5-18"
)

// helperStateDir holds helper.json and the replay register; its protected DACL
// grants only SYSTEM and Administrators.
var helperStateDir = filepath.Join(filepath.Dir(machineStateDir()), "helper")

// helperPipeClientAccess lets the agent read and write the pipe but not create
// pipe instances: FILE_GENERIC_READ | FILE_WRITE_DATA. The agent opens the pipe
// with exactly this mask; GENERIC_WRITE would include FILE_APPEND_DATA, which
// on a pipe is FILE_CREATE_PIPE_INSTANCE.
const helperPipeClientAccess = 0x00120089 | 0x00000002

func helperSupported() bool { return true }

type pipeAddress string

func (p pipeAddress) Network() string { return "pipe" }
func (p pipeAddress) String() string  { return string(p) }

// windowsPipeConn is a net.Conn over an overlapped named-pipe handle, written
// without go-winio. Each operation owns its OVERLAPPED and event until
// cancellation completes.
// Close cancels before acquiring either operation's serialization lock.
type windowsPipeConn struct {
	h                           windows.Handle
	name                        string
	mu                          sync.Mutex
	closed                      bool
	readDeadline, writeDeadline time.Time
	readMu, writeMu             sync.Mutex
}

func newWindowsPipeConn(h windows.Handle, name string) *windowsPipeConn {
	return &windowsPipeConn{h: h, name: name}
}

func (c *windowsPipeConn) operation(p []byte, write, connect bool) (int, error) {
	lock := &c.readMu
	if write {
		lock = &c.writeMu
	}
	lock.Lock()
	defer lock.Unlock()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	over := windows.Overlapped{HEvent: event}
	var n uint32
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	deadline := c.readDeadline
	if write {
		deadline = c.writeDeadline
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	if connect {
		err = windows.ConnectNamedPipe(c.h, &over)
		if err == windows.ERROR_PIPE_CONNECTED {
			err = nil
		}
	} else if write {
		err = windows.WriteFile(c.h, p, &n, &over)
	} else {
		err = windows.ReadFile(c.h, p, &n, &over)
	}
	c.mu.Unlock()
	if err == windows.ERROR_IO_PENDING {
		// Bounded waits observe both Close and deadlines changed during I/O.
		for {
			c.mu.Lock()
			deadline := c.readDeadline
			if write {
				deadline = c.writeDeadline
			}
			var stopped error
			if c.closed {
				stopped = net.ErrClosed
			} else if !deadline.IsZero() && !time.Now().Before(deadline) {
				stopped = os.ErrDeadlineExceeded
			}
			if stopped != nil {
				_ = windows.CancelIoEx(c.h, &over)
			}
			c.mu.Unlock()
			if stopped != nil {
				// Drain cancellation before releasing the buffer and OVERLAPPED.
				_ = windows.GetOverlappedResult(c.h, &over, &n, true)
				return int(n), stopped
			}
			state, waitErr := windows.WaitForSingleObject(event, 10)
			if waitErr != nil {
				_ = windows.CancelIoEx(c.h, &over)
				_ = windows.GetOverlappedResult(c.h, &over, &n, true)
				return int(n), waitErr
			}
			if state == windows.WAIT_OBJECT_0 {
				break
			}
		}
		err = windows.GetOverlappedResult(c.h, &over, &n, false)
	}
	if err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_PIPE_NOT_CONNECTED {
		err = io.EOF
	}
	return int(n), err
}

func (c *windowsPipeConn) Read(p []byte) (int, error)  { return c.operation(p, false, false) }
func (c *windowsPipeConn) Write(p []byte) (int, error) { return c.operation(p, true, false) }
func (c *windowsPipeConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	_ = windows.CancelIoEx(c.h, nil)
	c.mu.Unlock()
	// Keep the handle valid until both operations have drained cancellation.
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return windows.CloseHandle(c.h)
}
func (c *windowsPipeConn) LocalAddr() net.Addr  { return pipeAddress(c.name) }
func (c *windowsPipeConn) RemoteAddr() net.Addr { return pipeAddress(c.name) }
func (c *windowsPipeConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline, c.writeDeadline = t, t
	return nil
}
func (c *windowsPipeConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	return nil
}
func (c *windowsPipeConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return nil
}

type windowsPipeListener struct {
	name, sddl string
	mu         sync.Mutex
	acceptMu   sync.Mutex
	pending    *windowsPipeConn
	closed     bool
}

// listenWindowsHelper creates the helper pipe owned by owner (SYSTEM in
// production). The agent checks that owner before it sends anything: a
// non-administrator cannot create a pipe owned by another account, and the
// DACL does not let clients add pipe instances.
func listenWindowsHelper(name, owner string, allowed ...string) (net.Listener, error) {
	for _, sid := range append([]string{owner}, allowed...) {
		if _, err := windows.StringToSid(sid); err != nil {
			return nil, err
		}
	}
	sddl := "O:" + owner + "D:P(A;;GA;;;SY)(A;;GA;;;" + owner + ")"
	for _, sid := range allowed {
		sddl += fmt.Sprintf("(A;;0x%x;;;%s)", helperPipeClientAccess, sid)
	}
	l := &windowsPipeListener{name: name, sddl: sddl}
	c, err := l.instance(true)
	if err != nil {
		return nil, err
	}
	l.pending = c
	return l, nil
}
func (l *windowsPipeListener) instance(first bool) (*windowsPipeConn, error) {
	sd, err := windows.SecurityDescriptorFromString(l.sddl)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateNamedPipe(name, flags, windows.PIPE_REJECT_REMOTE_CLIENTS, 255, 65536, 65536, 0, &sa)
	if err != nil {
		return nil, err
	}
	return newWindowsPipeConn(h, l.name), nil
}
func (l *windowsPipeListener) Accept() (net.Conn, error) {
	l.acceptMu.Lock()
	defer l.acceptMu.Unlock()
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, net.ErrClosed
	}
	c := l.pending
	l.mu.Unlock()
	if _, err := c.operation(nil, false, true); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, net.ErrClosed
	}
	// Retain the pipe name before handing this instance to the caller.
	next, err := l.instance(false)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	l.pending = next
	return c, nil
}
func (l *windowsPipeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	c := l.pending
	l.mu.Unlock()
	return c.Close()
}
func (l *windowsPipeListener) Addr() net.Addr { return pipeAddress(l.name) }

// pipeClientSID returns the account of the process at the other end of a
// server-side pipe instance. The helper runs as SYSTEM and may query any
// client token.
func pipeClientSID(c net.Conn) (string, error) {
	pipe, ok := c.(*windowsPipeConn)
	if !ok {
		return "", errors.New("not a Windows pipe")
	}
	pipe.mu.Lock()
	defer pipe.mu.Unlock()
	if pipe.closed {
		return "", net.ErrClosed
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(pipe.h, &pid); err != nil {
		return "", err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// pipeOwnerSID returns the owner of the pipe a client opened. All instances of
// a pipe share the security descriptor of the first one.
func pipeOwnerSID(h windows.Handle) (string, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", err
	}
	if owner == nil {
		return "", errors.New("the pipe has no owner")
	}
	return owner.String(), nil
}

func peerSIDIn(allowed ...string) func(net.Conn) error {
	return func(c net.Conn) error {
		sid, err := pipeClientSID(c)
		if err != nil {
			return err
		}
		if !slices.Contains(allowed, sid) {
			return fmt.Errorf("unexpected pipe client SID %s", sid)
		}
		return nil
	}
}

// dialWindowsHelper opens the pipe for identification only (the helper cannot
// act as the agent) and accepts it only when ownerSID owns it.
func dialWindowsHelper(pipe, ownerSID string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		name, err := windows.UTF16PtrFromString(pipe)
		if err != nil {
			return nil, err
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			h, err := windows.CreateFile(name, helperPipeClientAccess, 0, nil, windows.OPEN_EXISTING,
				windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
			if err == nil {
				sid, err := pipeOwnerSID(h)
				if err != nil || sid != ownerSID {
					windows.CloseHandle(h)
					return nil, fmt.Errorf("the helper pipe is not owned by the expected account (owner %q): %v", sid, err)
				}
				return newWindowsPipeConn(h, pipe), nil
			}
			if err != windows.ERROR_PIPE_BUSY {
				return nil, err
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
}
func newLocalHelperClient(agentID string, logger *slog.Logger) privilegedRunner {
	return &helperClient{dial: dialWindowsHelper(helperSocketPath, systemSID), agentID: agentID, logger: logger}
}

// secureHelperParent makes %ProgramData%\NERVE administrator-controlled. By
// default standard users may create folders in %ProgramData%, and an owner of
// the parent could rename the helper directory. The parent must already be
// owned by SYSTEM or Administrators; its DACL is replaced so that others may
// only read it. The agent and helper directories keep their own protected DACLs.
func secureHelperParent(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !trustedHelperOwner(owner.String()) {
		return fmt.Errorf("%s is owned by an account other than SYSTEM or Administrators; remove it and install again", dir)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;0x1200a9;;;BU)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	return checkHelperSecurity(dir)
}

func trustedHelperOwner(sid string) bool { return sid == systemSID || sid == "S-1-5-32-544" }

// checkHelperSecurity refuses helper state that a non-administrator owns or
// may modify. Unknown ACE forms and reparse points fail closed.
func checkHelperSecurity(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("helper state cannot be a reparse point")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	trusted := trustedHelperOwner
	if owner == nil || !trusted(owner.String()) {
		return fmt.Errorf("untrusted owner of %s", path)
	}
	defer runtime.KeepAlive(sd)
	if info.IsDir() {
		control, _, err := sd.Control()
		if err != nil {
			return err
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			return errors.New("helper directory DACL must be protected")
		}
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("helper state has a null DACL")
	}
	// Includes generic write/all, DELETE, WRITE_DAC, WRITE_OWNER, file writes,
	// directory creation and FILE_DELETE_CHILD (which permits pin replacement).
	const writes = 0x40000000 | 0x10000000 | 0x000d0156
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported helper state ACE")
		}
		if uint32(ace.Mask)&writes != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()) {
			return fmt.Errorf("%s is writable by an untrusted SID", path)
		}
	}
	return nil
}

// loadHelperPin also checks the parent directory: whoever may change it could
// rename the helper directory and swap in another one.
func loadHelperPin(dir string) (helperPin, error) {
	for _, path := range []string{filepath.Dir(dir), dir, filepath.Join(dir, helperPinFileName)} {
		if err := checkHelperSecurity(path); err != nil {
			return helperPin{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, helperPinFileName))
	if err != nil {
		return helperPin{}, err
	}
	return parseHelperPin(raw)
}
func writeHelperPin(dir string, pin helperPin) error {
	raw, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".helper-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, helperPinFileName))
}

func helperInstalled() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(helperServiceName)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// installHelper pins the enrolled identity and installs the LocalSystem helper
// service, with the same pin rules as on Linux: an existing pin changes only
// after an enrollment this installer made itself (enrolledNow).
func installHelper(executable string, id *identity, enrolledNow, noStart bool) error {
	if id == nil {
		return errors.New("the privileged helper needs an enrolled agent; install with --token-file first")
	}
	if err := secureHelperParent(filepath.Dir(helperStateDir)); err != nil {
		return err
	}
	pin := newHelperPin(id)
	previous, err := loadHelperPin(helperStateDir)
	if err == nil && !enrolledNow {
		if previous.AgentID != pin.AgentID || !slices.ContainsFunc(previous.keys, func(k ed25519.PublicKey) bool { return k.Equal(id.coreKey) }) {
			return errors.New("the agent identity or Core key changed; re-enroll with --token-file and --force")
		}
		pin = previous
	} else if err != nil && !enrolledNow {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("existing helper pin unusable: %w", err)
		}
		if helperInstalled() {
			// An installed helper without its pin was tampered with; only a new
			// enrollment may establish trust again.
			return errors.New("the installed helper has no pin; re-enroll with --token-file and --force")
		}
		fmt.Printf("The helper trusts Core key %s from the existing identity; compare it with NERVE before enabling privileged commands.\n", agentproto.CoreKeyID(id.coreKey))
	}
	if _, err := os.Lstat(helperStateDir); err == nil {
		if err := checkHelperSecurity(helperStateDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(helperStateDir, 0o700); err != nil {
		return err
	}
	if err := restrictDir(helperStateDir); err != nil {
		return err
	}
	if err := checkHelperSecurity(helperStateDir); err != nil {
		return err
	}
	if err := writeHelperPin(helperStateDir, pin); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	config := mgr.Config{DisplayName: "NERVE Agent privileged helper", Description: "Runs operator-approved privileged NERVE commands for the NERVE Agent.",
		StartType: mgr.StartAutomatic, ServiceStartName: "LocalSystem", ErrorControl: mgr.ErrorNormal}
	s, err := m.OpenService(helperServiceName)
	if err == nil {
		defer s.Close()
		if _, err := stopService(s); err != nil {
			return err
		}
		current, err := s.Config()
		if err != nil {
			return err
		}
		config.ServiceType = current.ServiceType
		config.BinaryPathName = commandLine(executable, []string{"helper"})
		if err := s.UpdateConfig(config); err != nil {
			return err
		}
	} else {
		s, err = m.CreateService(helperServiceName, executable, config, "helper")
		if err != nil {
			return err
		}
		defer s.Close()
	}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 86400); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	if err := eventlog.InstallAsEventCreate(helperServiceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil && !strings.Contains(err.Error(), "exists") {
		return err
	}
	if noStart {
		return nil
	}
	if err := s.Start(); err != nil {
		return err
	}
	_, err = waitForState(s, svc.Running)
	return err
}
func uninstallHelper(purge bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(helperServiceName)
	if err == nil {
		defer s.Close()
		if _, err := stopService(s); err != nil {
			return err
		}
		if err := s.Delete(); err != nil {
			return err
		}
	} else if err != windows.ERROR_SERVICE_DOES_NOT_EXIST {
		return err
	}
	_ = eventlog.Remove(helperServiceName)
	if purge {
		return os.RemoveAll(helperStateDir)
	}
	return nil
}

type windowsHelperService struct {
	serve func(context.Context) error
}

func (s *windowsHelperService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done:
			return true, 1
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 10000}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}
func runHelper(args []string) int {
	flags := flag.NewFlagSet("helper", flag.ContinueOnError)
	dir := flags.String("state-dir", helperStateDir, "directory with helper.json and the replay register")
	pipe := flags.String("pipe", helperSocketPath, "local helper pipe")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	logger := newLogger(os.Stdout, new(slog.LevelVar))
	serve := func(ctx context.Context) error {
		pin, err := loadHelperPin(*dir)
		if err != nil {
			return err
		}
		replayPath := filepath.Join(*dir, helperReplayFileName)
		if err := checkHelperSecurity(replayPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		replay, err := openReplayStore(replayPath)
		if err != nil {
			return err
		}
		sid, err := serviceAccountSID()
		if err != nil {
			return err
		}
		listener, err := listenWindowsHelper(*pipe, systemSID, sid)
		if err != nil {
			return err
		}
		defer listener.Close()
		server := &helperServer{pin: pin, replay: replay, handlers: helperHandlers, logger: logger, now: time.Now, authorized: peerSIDIn(sid, systemSID)}
		return server.serve(ctx, listener)
	}
	if runningAsService() {
		events, _ := eventlog.Open(helperServiceName)
		if events != nil {
			defer events.Close()
		}
		wrapped := func(ctx context.Context) error {
			err := serve(ctx)
			if err != nil && events != nil {
				_ = events.Error(1, err.Error())
			}
			return err
		}
		if err := svc.Run(helperServiceName, &windowsHelperService{serve: wrapped}); err != nil {
			logger.Error("helper service failed", "error", err)
			return 1
		}
		return 0
	}
	if err := requireAdministrator(); err != nil {
		logger.Error("helper requires elevation", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := serve(ctx); err != nil {
		logger.Error("helper stopped", "error", err)
		return 1
	}
	return 0
}
func diagnoseHelper(ctx context.Context, result *diagnosis, agentID string) {
	m, err := mgr.Connect()
	if err != nil {
		result.add("helper", "fail", "%v", err)
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(helperServiceName)
	if err != nil {
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			result.add("helper", "skip", "not installed; privileged commands fail closed (install with --with-helper)")
		} else {
			result.add("helper", "fail", "%v", err)
		}
		return
	}
	defer s.Close()
	state, err := s.Query()
	if err != nil {
		result.add("helper", "fail", "%v", err)
		return
	}
	if state.State != svc.Running {
		result.add("helper", "fail", "service %s", stateName(state.State))
		return
	}
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := &helperClient{dial: dialWindowsHelper(helperSocketPath, systemSID), agentID: agentID}
	response, err := client.exchange(check, helperRequest{Op: "health"})
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		// The pipe admits only the agent service and SYSTEM, so an administrator
		// console cannot reach it; that is the intended boundary, not a fault.
		result.add("helper", "skip", "service running; its pipe admits only NT SERVICE\\NERVEAgent and SYSTEM, so this console cannot check it (NERVE shows system actions for the device when the agent reaches it)")
	case err != nil:
		result.add("helper", "fail", "installed but not answering: %v", err)
	case response.Health == nil:
		result.add("helper", "fail", "unexpected answer: %s", response.Code)
	case agentID != "" && response.Health.AgentID != agentID:
		result.add("helper", "fail", "serves agent %s, not %s; run `service install --with-helper` again", response.Health.AgentID, agentID)
	case len(response.Health.Types) == 0:
		result.add("helper", "ok", "version %s, channel SYSTEM; no privileged command types enabled yet", response.Health.Version)
	default:
		result.add("helper", "ok", "version %s, channel SYSTEM; runs %v", response.Health.Version, response.Health.Types)
	}
}
