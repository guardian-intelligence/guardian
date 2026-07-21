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
	// luks marks devices carrying a LUKS header; luksBlank marks LUKS
	// devices whose inner volume has no filesystem yet.
	luks      map[string]bool
	luksBlank map[string]bool
	// mounted maps mountpoint -> device.
	mounted map[string]string
	syncs   int
	journal []string
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		devices:   map[string]string{},
		blank:     map[string]bool{},
		luks:      map[string]bool{},
		luksBlank: map[string]bool{},
		mounted:   map[string]string{},
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

func (f *fakeSystem) Discard(_ context.Context, device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blank[device] = true
	f.log("discard %s", device)
	return nil
}

func (f *fakeSystem) IsLUKS(_ context.Context, device string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.luks[device], nil
}

func (f *fakeSystem) FormatLUKS(_ context.Context, device string, key []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.luks[device] = true
	f.luksBlank[device] = true
	f.log("luksFormat %s keylen=%d", device, len(key))
	return nil
}

func (f *fakeSystem) OpenLUKS(_ context.Context, device, name string, key []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.luks[device] {
		return "", errors.New("not a LUKS device")
	}
	mapper := "/dev/mapper/" + name
	if _, ok := f.devices["mapper:"+name]; !ok {
		f.devices["mapper:"+name] = mapper
		f.blank[mapper] = f.luksBlank[device]
		f.log("luksOpen %s as %s keylen=%d", device, name, len(key))
	}
	return mapper, nil
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
	for {
		message := h.expect(guestproto.KindRunnerStatus)
		if message.RunnerStatus.State == guestproto.RunnerProgress {
			continue
		}
		if message.RunnerStatus.State != state {
			h.t.Fatalf("runner state %s, want %s", message.RunnerStatus.State, state)
		}
		return *message.RunnerStatus
	}
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
			w.runs <- runnerCall{jitConfig: jitConfig, env: env}
			event(EventListening)
			if reply := w.server.publishAssignment(ptr(testAssignment())); reply.Error != "" {
				return 0, errors.New(reply.Error)
			}
			if reply := w.server.awaitWorker(context.Background()); reply.Error != "" {
				return 0, errors.New(reply.Error)
			}
			if reply := w.server.validateAssignment(testAuthorize().Identity); reply.Error != "" {
				return 0, errors.New(reply.Error)
			}
			if reply := w.server.releaseAssignment(testAuthorize().Identity); reply.Error != "" {
				return 0, errors.New(reply.Error)
			}
			return 0, nil
		}
	}
	cfg := Config{
		System:        w.system,
		RunRunner:     runner,
		MountDeadline: 2 * time.Second,
		RetryInterval: time.Millisecond,
		QuiesceWindow: 20 * time.Millisecond,
		HookDeadline:  2 * time.Second,
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

func testPrepare() guestproto.Prepare {
	return guestproto.Prepare{
		Lease: "lease-1", JITConfig: "jit-blob",
		Env: map[string]string{"POSTFLIGHT_POOL": "fixture"},
	}
}

func testAssignment() guestproto.Assignment {
	return guestproto.Assignment{
		RequestID: "request-1", JobID: "job-1", RunnerName: "lease-1", JobDisplayName: "test",
		Identity: testAuthorize().Identity,
	}
}

func testRendezvous() guestproto.Rendezvous {
	return guestproto.Rendezvous{
		Lease: "lease-1",
		Mounts: []guestproto.Mount{{
			Serial:     "workspace",
			Filesystem: "ext4",
			Mountpoint: "/work",
			Options:    []string{"discard", "noatime", "nodev", "nosuid"},
		}},
	}
}

func testAuthorize() guestproto.Authorize {
	return guestproto.Authorize{
		Lease: "lease-1", RequestID: "request-1",
		Identity: &guestproto.JobIdentity{
			RunID: "101", RunAttempt: 1, RunnerName: "lease-1",
			Repository: "acme/widget", WorkflowJob: "test",
		},
		Env: map[string]string{"POSTFLIGHT_EXECUTION_ID": "exec-1"},
	}
}

func sendRendezvousLifecycle(host *hostConn) {
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
	host.expectStatus(guestproto.RunnerRegistered)
	assignment := host.expect(guestproto.KindAssignment)
	if assignment.Assignment.RequestID != testAssignment().RequestID {
		host.t.Fatalf("assignment = %#v", assignment.Assignment)
	}
	host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: ptr(testRendezvous())})
	host.expectStatus(guestproto.RunnerMountsReady)
	host.send(guestproto.Message{Kind: guestproto.KindAuthorize, Authorize: ptr(testAuthorize())})
	host.expectStatus(guestproto.RunnerWorkerReady)
	host.expectStatus(guestproto.RunnerHookBlocked)
	host.expectStatus(guestproto.RunnerReleased)
}

func TestColdWorkspaceLifecycle(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = true

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	sendRendezvousLifecycle(host)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}

	run := <-w.runs
	if run.jitConfig != "jit-blob" || run.env["POSTFLIGHT_POOL"] == "" {
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
	sendRendezvousLifecycle(host)
	host.expectStatus(guestproto.RunnerExited)

	for _, entry := range w.system.entries() {
		if strings.HasPrefix(entry, "mkfs") {
			t.Fatalf("reformatted a warm workspace: %v", w.system.entries())
		}
	}
}

func TestEncryptedColdWorkspaceLifecycle(t *testing.T) {
	w := newWorld(t, func(c *Config) { c.Encryption = EncryptionDev }, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = true

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	sendRendezvousLifecycle(host)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}

	entries := w.system.entries()
	want := []string{
		"discard /dev/sdb",
		"luksFormat /dev/sdb keylen=32",
		"luksOpen /dev/sdb as pf-workspace keylen=32",
		"mkfs ext4 /dev/mapper/pf-workspace",
		"mount /dev/mapper/pf-workspace at /work options=discard,noatime,nodev,nosuid",
		"adopt /work",
	}
	if fmt.Sprint(entries) != fmt.Sprint(want) {
		t.Fatalf("journal %v, want %v", entries, want)
	}
}

func TestEncryptedWarmWorkspaceReopensWithoutReformat(t *testing.T) {
	w := newWorld(t, func(c *Config) { c.Encryption = EncryptionDev }, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = false
	w.system.luks["/dev/sdb"] = true

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	sendRendezvousLifecycle(host)
	host.expectStatus(guestproto.RunnerExited)

	for _, entry := range w.system.entries() {
		if strings.HasPrefix(entry, "mkfs") || strings.HasPrefix(entry, "luksFormat") {
			t.Fatalf("reformatted a warm encrypted workspace: %v", w.system.entries())
		}
	}
}

func TestPlaintextLineageIsReformattedUnderEncryption(t *testing.T) {
	// A generation sealed before the encryption cutover arrives as a
	// plaintext filesystem; the workspace is rebuildable cache, so the
	// ladder reformats it to LUKS and the job cold-builds.
	w := newWorld(t, func(c *Config) { c.Encryption = EncryptionDev }, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = false

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	sendRendezvousLifecycle(host)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}
	entries := w.system.entries()
	want := []string{
		"discard /dev/sdb",
		"luksFormat /dev/sdb keylen=32",
		"luksOpen /dev/sdb as pf-workspace keylen=32",
		"mkfs ext4 /dev/mapper/pf-workspace",
		"mount /dev/mapper/pf-workspace at /work options=discard,noatime,nodev,nosuid",
		"adopt /work",
	}
	if fmt.Sprint(entries) != fmt.Sprint(want) {
		t.Fatalf("journal %v, want %v", entries, want)
	}
}

func TestMountConvergesThroughUdevLag(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	w.system.blank["/dev/sdb"] = true
	w.system.locateFailures = 5

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	sendRendezvousLifecycle(host)
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != 0 {
		t.Fatalf("exit code %d", status.ExitCode)
	}
}

func TestMountThatCannotConvergeReportsSyntheticExit(t *testing.T) {
	w := newWorld(t, func(cfg *Config) { cfg.MountDeadline = 30 * time.Millisecond }, nil)
	// No device ever appears.

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
	host.expectStatus(guestproto.RunnerRegistered)
	host.expect(guestproto.KindAssignment)
	host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: ptr(testRendezvous())})
	if status := host.expectStatus(guestproto.RunnerExited); status.ExitCode != SyntheticFailureExitCode {
		t.Fatalf("exit code %d, want the synthetic %d", status.ExitCode, SyntheticFailureExitCode)
	}
	w.server.mu.Lock()
	gateErr := w.server.gateErr
	w.server.mu.Unlock()
	if gateErr == nil {
		t.Fatal("failed rendezvous did not reject Runner.Worker")
	}
}

func TestDiscardIsEnforcedOnTheMount(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"
	rendezvous := testRendezvous()
	rendezvous.Mounts[0].Options = []string{"noatime"}

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
	host.expectStatus(guestproto.RunnerRegistered)
	host.expect(guestproto.KindAssignment)
	host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: &rendezvous})
	host.expectStatus(guestproto.RunnerMountsReady)
	host.send(guestproto.Message{Kind: guestproto.KindAuthorize, Authorize: ptr(testAuthorize())})
	host.expectStatus(guestproto.RunnerWorkerReady)
	host.expectStatus(guestproto.RunnerHookBlocked)
	host.expectStatus(guestproto.RunnerReleased)
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

func TestPreparationAndRendezvousRedeliveryAreDeduped(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.devices["workspace"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
	// A different lease on a single-use VM is ignored, not executed.
	conflicting := testPrepare()
	conflicting.Lease = "lease-2"
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: &conflicting})

	host.expectStatus(guestproto.RunnerRegistered)
	host.expect(guestproto.KindAssignment)
	host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: ptr(testRendezvous())})
	host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: ptr(testRendezvous())})
	host.expectStatus(guestproto.RunnerMountsReady)
	host.send(guestproto.Message{Kind: guestproto.KindAuthorize, Authorize: ptr(testAuthorize())})
	host.expectStatus(guestproto.RunnerWorkerReady)
	host.expectStatus(guestproto.RunnerHookBlocked)
	host.expectStatus(guestproto.RunnerReleased)
	host.expectStatus(guestproto.RunnerExited)
	<-w.runs
	select {
	case run := <-w.runs:
		t.Fatalf("assignment ran twice: %+v", run)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestQuiesceRequiresMountedVolumesAndSyncs(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.mounted["/work"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoints: []string{"/work"}}})
	message := host.expect(guestproto.KindQuiesced)
	wantTiming := []string{"quiesce_received", "quiesce_mounts_checked", "filesystem_sync_started", "filesystem_sync_completed"}
	if len(message.Quiesced.Timing) != len(wantTiming) {
		t.Fatalf("quiesce timing %+v, want %v", message.Quiesced.Timing, wantTiming)
	}
	for i, want := range wantTiming {
		if got := message.Quiesced.Timing[i]; got.Event != want || got.MonotonicNS <= 0 || got.UnixNS <= 0 {
			t.Fatalf("quiesce timing[%d] %+v, want %s with clocks", i, got, want)
		}
	}
	if mounted, _ := w.system.IsMounted("/work"); !mounted {
		t.Fatal("workspace was unmounted before QEMU teardown")
	}
	if w.system.syncs == 0 {
		t.Fatal("quiesce never synced")
	}
	// A retried quiesce remains idempotent while the VM is alive.
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoints: []string{"/work"}}})
	host.expect(guestproto.KindQuiesced)
}

func TestQuiesceFailureCarriesTheReason(t *testing.T) {
	w := newWorld(t, nil, nil)

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoints: []string{"/work"}}})
	message := host.expect(guestproto.KindQuiesceFailed)
	if !strings.Contains(message.QuiesceFailed.Reason, "required volume is not mounted") {
		t.Fatalf("reason %q", message.QuiesceFailed.Reason)
	}
	if len(message.QuiesceFailed.Timing) != 1 || message.QuiesceFailed.Timing[0].Event != "quiesce_received" {
		t.Fatalf("failure timing %+v", message.QuiesceFailed.Timing)
	}
}

func TestCheckpointRefusesAnIncompleteGenerationTuple(t *testing.T) {
	w := newWorld(t, nil, nil)
	w.system.mounted["/work"] = "/dev/sdb"

	host := w.listener.dial(t)
	host.expect(guestproto.KindHello)
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{
		Mountpoints: []string{"/work", ProcessMountpoint},
		Checkpoint: &guestproto.CheckpointDump{
			ImagesDir: ProcessImagesDir, ExternalMountAt: "/work",
		},
	}})
	message := host.expect(guestproto.KindQuiesceFailed)
	if !strings.Contains(message.QuiesceFailed.Reason, "required volume is not mounted at "+ProcessMountpoint) {
		t.Fatalf("reason %q", message.QuiesceFailed.Reason)
	}
	if w.system.syncs != 0 {
		t.Fatal("incomplete generation was flushed as sealable")
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
	sendRendezvousLifecycle(host)
	host.expectStatus(guestproto.RunnerExited)
	host.conn.Close()

	second := w.listener.dial(t)
	second.expect(guestproto.KindHello)
	second.expect(guestproto.KindAssignment)
	second.expectStatus(guestproto.RunnerRegistered)
	second.expectStatus(guestproto.RunnerMountsReady)
	second.expectStatus(guestproto.RunnerWorkerReady)
	second.expectStatus(guestproto.RunnerHookBlocked)
	second.expectStatus(guestproto.RunnerReleased)
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

	sendRendezvousLifecycle(second)
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
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
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
	host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
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
	host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoints: []string{"/work"}}})
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
			host.send(guestproto.Message{Kind: guestproto.KindQuiesce, Quiesce: &guestproto.Quiesce{Mountpoints: []string{mountpoint}}})
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
			rendezvous := testRendezvous()
			mutate(&rendezvous.Mounts[0])

			host := w.listener.dial(t)
			host.expect(guestproto.KindHello)
			host.send(guestproto.Message{Kind: guestproto.KindPrepare, Prepare: ptr(testPrepare())})
			host.expectStatus(guestproto.RunnerRegistered)
			host.expect(guestproto.KindAssignment)
			host.send(guestproto.Message{Kind: guestproto.KindRendezvous, Rendezvous: &rendezvous})
			time.Sleep(30 * time.Millisecond)
			if entries := w.system.entries(); len(entries) != 0 {
				t.Fatalf("hostile spec reached the system: %v", entries)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
