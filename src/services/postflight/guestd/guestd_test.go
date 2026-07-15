package guestd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
)

// fakeSystem is the hermetic System: devices appear on a schedule, mounts
// and mkfs are bookkeeping, and everything lands in an ordered journal.
type fakeSystem struct {
	mu sync.Mutex
	// devices maps serial -> device path once the device has "appeared".
	devices map[string]string
	// locateFailures fails that many LocateDevice calls before the device
	// appears — the udev-lag shape.
	locateFailures int
	// blank marks devices carrying no filesystem signature.
	blank map[string]bool
	// mounted maps mountpoint -> device.
	mounted map[string]string
	// unmountErr, when set, fails every Unmount.
	unmountErr error
	syncs      int
	journal    []string
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		devices: map[string]string{},
		blank:   map[string]bool{},
		mounted: map[string]string{},
	}
}

func (f *fakeSystem) log(format string, args ...any) {
	f.journal = append(f.journal, fmt.Sprintf(format, args...))
}

func (f *fakeSystem) LocateDevice(_ context.Context, serial string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locateFailures > 0 {
		f.locateFailures--
		return "", errors.New("no such device yet")
	}
	device, ok := f.devices[serial]
	if !ok {
		return "", errors.New("unknown serial")
	}
	return device, nil
}

func (f *fakeSystem) IsBlank(_ context.Context, device string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blank[device], nil
}

func (f *fakeSystem) MakeFilesystem(_ context.Context, device, filesystem string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blank[device] = false
	f.log("mkfs %s %s", filesystem, device)
	return nil
}

func (f *fakeSystem) IsMounted(mountpoint string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.mounted[mountpoint]
	return ok, nil
}

func (f *fakeSystem) Mount(_ context.Context, device, mountpoint, filesystem string, options []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mounted[mountpoint] = device
	f.log("mount %s at %s options=%s", device, mountpoint, strings.Join(options, ","))
	return nil
}

func (f *fakeSystem) Unmount(mountpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unmountErr != nil {
		return f.unmountErr
	}
	delete(f.mounted, mountpoint)
	f.log("unmount %s", mountpoint)
	return nil
}

func (f *fakeSystem) Sync() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs++
	f.log("sync")
}

func (f *fakeSystem) Adopt(mountpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("adopt %s", mountpoint)
	return nil
}

func (f *fakeSystem) entries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.journal...)
}

// pipeListener feeds in-memory connections to Server.Serve.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "pipe", Net: "unix"} }

func (l *pipeListener) dial(t *testing.T) *hostConn {
	t.Helper()
	return l.dialConn(t, nil)
}

// dialFrom presents the guest side of the pipe as a vsock peer at cid.
func (l *pipeListener) dialFrom(t *testing.T, cid uint32) *hostConn {
	t.Helper()
	return l.dialConn(t, func(conn net.Conn) net.Conn {
		return addrConn{Conn: conn, remote: vsock.Addr{CID: cid, Port: 12345}}
	})
}

func (l *pipeListener) dialConn(t *testing.T, wrap func(net.Conn) net.Conn) *hostConn {
	t.Helper()
	host, guest := net.Pipe()
	served := net.Conn(guest)
	if wrap != nil {
		served = wrap(guest)
	}
	select {
	case l.conns <- served:
	case <-time.After(2 * time.Second):
		t.Fatal("server never accepted")
	}
	return &hostConn{t: t, conn: host, decoder: guestproto.NewDecoder(host), encoder: guestproto.NewEncoder(host)}
}

// addrConn overrides a pipe's remote address so peer checks see a vsock CID.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

// hostConn is the test's hostd stand-in on one connection.
type hostConn struct {
	t       *testing.T
	conn    net.Conn
	decoder *guestproto.Decoder
	encoder *guestproto.Encoder
}

func (h *hostConn) expect(kind guestproto.Kind) guestproto.Message {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	message, err := h.decoder.Read()
	if err != nil {
		h.t.Fatalf("reading %s: %v", kind, err)
	}
	if message.Kind != kind {
		h.t.Fatalf("got %s, want %s", message.Kind, kind)
	}
	return message
}

func (h *hostConn) expectStatus(state guestproto.RunnerState) guestproto.RunnerStatus {
	h.t.Helper()
	message := h.expect(guestproto.KindRunnerStatus)
	if message.RunnerStatus.State != state {
		h.t.Fatalf("runner state %s, want %s", message.RunnerStatus.State, state)
	}
	return *message.RunnerStatus
}

func (h *hostConn) send(message guestproto.Message) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := h.encoder.Write(message); err != nil {
		h.t.Fatalf("writing %s: %v", message.Kind, err)
	}
}

type runnerCall struct {
	jitConfig string
	env       map[string]string
}

// world wires a Server over fakes and starts it on a pipe listener.
type world struct {
	system   *fakeSystem
	listener *pipeListener
	server   *Server
	runs     chan runnerCall
}

func newWorld(t *testing.T, configure func(*Config), runner RunRunner) *world {
	t.Helper()
	w := &world{system: newFakeSystem(), listener: newPipeListener(), runs: make(chan runnerCall, 4)}
	if runner == nil {
		runner = func(_ context.Context, jitConfig string, env map[string]string, event func(RunnerEvent)) (int, error) {
			if mounted, _ := w.system.IsMounted("/work"); !mounted {
				t.Error("runner started before the workspace mount converged")
			}
			w.runs <- runnerCall{jitConfig: jitConfig, env: env}
			event(EventListening)
			event(EventJobStarted)
			return 0, nil
		}
	}
	cfg := Config{
		System:        w.system,
		RunRunner:     runner,
		MountDeadline: 2 * time.Second,
		RetryInterval: time.Millisecond,
		QuiesceWindow: 20 * time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if configure != nil {
		configure(&cfg)
	}
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.server = server
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(ctx, w.listener)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return w
}

func testAssignment() guestproto.Assignment {
	return guestproto.Assignment{
		Lease: "lease-1",
		Mounts: []guestproto.Mount{{
			Serial:     "workspace",
			Filesystem: "ext4",
			Mountpoint: "/work",
			Options:    []string{"discard", "noatime", "nodev", "nosuid"},
		}},
		JITConfig: "jit-blob",
		Env:       map[string]string{"POSTFLIGHT_EXECUTION_ID": "exec-1"},
	}
}

func TestColdWorkspaceLifecycle(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = true

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})

	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}

	run := <-w.runs
	if run.jitConfig != "jit-blob" || run.env["POSTFLIGHT_EXECUTION_ID"] != "exec-1" {
		t.Fatalf("runner ran with %+v", run)
	}
	entries := w.system.entries()
	want := []string{
		"mkfs ext4 /dev/sdb",
		"mount /dev/sdb at /work options=discard,noatime,nodev,nosuid",
		"adopt /work",
	}
	if len(entries) != len(want) {
		t.Fatalf("journal %v, want %v", entries, want)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("journal %v, want %v", entries, want)
		}
	}
}

func TestWarmWorkspaceIsNotReformatted(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = false

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	host.expectStatus(guestproto.RunnerExited)

	for _, entry := range w.system.entries() {
		if strings.HasPrefix(entry, "mkfs") {
			t.Fatalf("reformatted a warm workspace: %v", w.system.entries())
		}
	}
}

func TestMountConvergesThroughUdevLag(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = true
	w.system.locateFailures = 5

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}
}

func TestMountThatCannotConvergeReportsSyntheticExit(t *testing.T) {
	w := newWorld(t, func(cfg *Config) { cfg.MountDeadline = 30 * time.Millisecond }, nil)
	// No device ever appears.

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != SyntheticFailureExitCode {
		t.Fatalf("exit code %d, want the synthetic %d", status.ExitCode, SyntheticFailureExitCode)
	}
	select {
	case run := <-w.runs:
		t.Fatalf("runner ran against a partial workspace: %+v", run)
	default:
	}
}

func TestDiscardIsEnforcedOnTheMount(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	assignment := testAssignment()
	assignment.Mounts[0].Options = []string{"noatime"}

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: &assignment})
	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	host.expectStatus(guestproto.RunnerExited)

	found := false
	for _, entry := range w.system.entries() {
		if strings.Contains(entry, "options=discard,noatime") {
			found = true
		}
	}
	if !found {
		t.Fatalf("discard not enforced: %v", w.system.entries())
	}
}

func TestAssignmentRedeliveryIsDeduped(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	// A different lease on a single-use VM is ignored, not executed.
	conflicting := testAssignment()
	conflicting.Lease = "lease-2"
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: &conflicting})

	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	host.expectStatus(guestproto.RunnerExited)
	<-w.runs
	select {
	case run := <-w.runs:
		t.Fatalf("assignment ran twice: %+v", run)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestQuiesceSyncsAndUnmounts(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.mounted["/work"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoint: "/work"}})
	host.expect(guestproto.KindQuiesced)
	if mounted, _ := w.system.IsMounted("/work"); mounted {
		t.Fatal("workspace still mounted after quiesce")
	}
	if w.system.syncs == 0 {
		t.Fatal("quiesce never synced")
	}
	// A retried quiesce (already unmounted) converges to success.
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoint: "/work"}})
	host.expect(guestproto.KindQuiesced)
}

func TestQuiesceFailureCarriesTheReason(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.mounted["/work"] = "/dev/sdb"
	w.system.unmountErr = errors.New("target is busy")

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoint: "/work"}})
	message := host.expect(guestproto.KindQuiesceFailed)
	if !strings.Contains(message.QuiesceFailed.Reason, "target is busy") {
		t.Fatalf("reason %q", message.QuiesceFailed.Reason)
	}
}

// TestReconnectReplaysTheRunnerLadder: a host that reconnects (hostd
// restart) is greeted and caught up on every status, so a runner exit
// observed while no host was connected is never lost — and neither is the
// registration that preceded it.
func TestReconnectReplaysTheRunnerLadder(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	host.expectStatus(guestproto.RunnerJobStarted)
	host.expectStatus(guestproto.RunnerExited)
	host.conn.Close()

	second := w.listener.dial(t)
	second.expect(guestproto.KindHello)
	second.expectStatus(guestproto.RunnerMounting)
	second.expectStatus(guestproto.RunnerRegistered)
	second.expectStatus(guestproto.RunnerJobStarted)
	if status := second.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("replayed %+v", status)
	}
}

// TestNewerConnectionSupplantsInArrivalOrder: the connection accepted last
// is the one that carries statuses, even when its predecessor's handler is
// still alive — a dying dial must never steal the channel back.
func TestNewerConnectionSupplantsInArrivalOrder(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"

	first := w.listener.dial(t)
	first.expect(guestproto.KindHello)
	second := w.listener.dial(t)
	second.expect(guestproto.KindHello)

	// The first connection was closed by the supplant.
	_ = first.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if message, err := first.decoder.Read(); err == nil {
		t.Fatalf("supplanted connection was served %s", message.Kind)
	}

	second.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	second.expectStatus(guestproto.RunnerMounting)
	second.expectStatus(guestproto.RunnerRegistered)
	second.expectStatus(guestproto.RunnerJobStarted)
	second.expectStatus(guestproto.RunnerExited)
}

func TestRunnerFailureToStartIsSynthetic(t *testing.T) {
	runner := func(context.Context, string, map[string]string, func(RunnerEvent)) (int, error) {
		return 0, errors.New("run.sh missing")
	}
	w := newWorld(t, nil, runner)
	w.system.devices["workspace"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != SyntheticFailureExitCode {
		t.Fatalf("exit code %d, want the synthetic %d", status.ExitCode, SyntheticFailureExitCode)
	}
}

func TestRunnerExitCodeIsVerbatim(t *testing.T) {
	runner := func(_ context.Context, _ string, _ map[string]string, event func(RunnerEvent)) (int, error) {
		event(EventListening)
		return 42, nil
	}
	w := newWorld(t, nil, runner)
	w.system.devices["workspace"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: ptr(testAssignment())})
	host.expectStatus(guestproto.RunnerMounting)
	host.expectStatus(guestproto.RunnerRegistered)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 42 {
		t.Fatalf("exit code %d, want 42", status.ExitCode)
	}
}

// TestNonHostVsockPeerIsRejected: anything inside the guest (the runner
// user included) can dial guestd's listener; only the configured host CID
// may drive privileged verbs, and a rejected peer never supplants the
// host's live connection.
func TestNonHostVsockPeerIsRejected(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.mounted["/work"] = "/dev/sdb"

	host := w.listener.dialFrom(t, vsock.Host)
	host.expect(guestproto.KindHello)

	intruder := w.listener.dialFrom(t, 3)
	_ = intruder.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if message, err := intruder.decoder.Read(); err == nil {
		t.Fatalf("intruder was served %s", message.Kind)
	}

	// The intruder never reached dispatch: the host connection is intact
	// and quiesce still works on it.
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoint: "/work"}})
	host.expect(guestproto.KindQuiesced)
}

func TestHostileQuiesceMountpointsAreRefused(t *testing.T) {
	for name, mountpoint := range map[string]string{
		"empty":    "",
		"relative": "work",
		"unclean":  "/work/../etc",
		"root":     "/",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, nil, nil)
			w.system.mounted["/work"] = "/dev/sdb"

			host := w.listener.dial(t)
			host.expect(guestproto.KindHello)
			host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoint: mountpoint}})
			host.expect(guestproto.KindQuiesceFailed)
			if entries := w.system.entries(); len(entries) != 0 {
				t.Fatalf("hostile quiesce reached the system: %v", entries)
			}
		})
	}
}

func TestHostileMountSpecsNeverReachTheSystem(t *testing.T) {
	for name, mutate := range map[string]func(*guestproto.Mount){
		"no serial":          func(m *guestproto.Mount) { m.Serial = "" },
		"no filesystem":      func(m *guestproto.Mount) { m.Filesystem = "" },
		"relative":           func(m *guestproto.Mount) { m.Mountpoint = "work" },
		"unclean":            func(m *guestproto.Mount) { m.Mountpoint = "/work/../etc" },
		"root":               func(m *guestproto.Mount) { m.Mountpoint = "/" },
		"trailing separator": func(m *guestproto.Mount) { m.Mountpoint = "/work/" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, func(cfg *Config) { cfg.MountDeadline = 20 * time.Millisecond }, nil)
			w.system.devices["workspace"] = "/dev/sdb"
			assignment := testAssignment()
			mutate(&assignment.Mounts[0])

			host := w.listener.dial(t)
			host.expect(guestproto.KindHello)
			host.send(guestproto.Message{Kind: guestproto.KindAssignment, Assignment: &assignment})
			host.expectStatus(guestproto.RunnerMounting)
			if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != SyntheticFailureExitCode {
				t.Fatalf("exit code %d, want the synthetic %d", status.ExitCode, SyntheticFailureExitCode)
			}
			if entries := w.system.entries(); len(entries) != 0 {
				t.Fatalf("hostile spec reached the system: %v", entries)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
