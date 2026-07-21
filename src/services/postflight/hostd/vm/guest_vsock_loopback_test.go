package vm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/guestd"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
	"golang.org/x/sys/unix"
)

// loopSystem is an in-memory guestd.System: just enough substrate for the
// loopback conformance of the transport, no privileges required.
type loopSystem struct {
	mu      sync.Mutex
	mounted map[string]bool
}

func (s *loopSystem) LocateDevice(context.Context, string) (string, error) { return "/dev/fake", nil }
func (s *loopSystem) IsBlank(context.Context, string) (bool, error)        { return true, nil }
func (s *loopSystem) IsLUKS(context.Context, string) (bool, error)         { return false, nil }
func (s *loopSystem) Discard(context.Context, string) error                { return nil }
func (s *loopSystem) FormatLUKS(context.Context, string, []byte) error     { return nil }
func (s *loopSystem) OpenLUKS(_ context.Context, _, name string, _ []byte) (string, error) {
	return "/dev/mapper/" + name, nil
}
func (s *loopSystem) MakeFilesystem(context.Context, string, string) error { return nil }

func (s *loopSystem) IsMounted(mountpoint string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounted[mountpoint], nil
}

func (s *loopSystem) Mount(_ context.Context, _, mountpoint, _ string, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mounted[mountpoint] = true
	return nil
}

func (s *loopSystem) Unmount(mountpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mounted, mountpoint)
	return nil
}

func (s *loopSystem) Sync()              {}
func (s *loopSystem) Adopt(string) error { return nil }

// TestVsockLoopbackTransportEndToEnd runs the real transport against a real
// guestd server over the kernel's vsock loopback: dial, hello probe,
// assignment, runner status stream, quiesce. Self-skips where AF_VSOCK or
// the vsock_loopback transport is unavailable (CI sandboxes have neither).
func TestVsockLoopbackTransportEndToEnd(t *testing.T) {
	listener, err := vsock.Listen(vsock.Any, vsock.PortAny)
	if err != nil {
		if errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENODEV) {
			t.Skipf("AF_VSOCK unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(vsock.Addr).Port

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer probeCancel()
	probe, err := vsock.Dial(probeCtx, vsock.Local, port)
	if err != nil {
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.EAFNOSUPPORT) {
			t.Skipf("vsock loopback unavailable: %v", err)
		}
		t.Fatal(err)
	}
	probe.Close()

	system := &loopSystem{mounted: map[string]bool{}}
	ran := make(chan string, 1)
	assignmentSocket := filepath.Join(t.TempDir(), "assignment.sock")
	identity := guestproto.JobIdentity{
		RunID: "1", RunAttempt: 1, RunnerName: "lease-loop",
		Repository: "acme/widget", WorkflowJob: "test",
	}
	var server *guestd.Server
	server, err = guestd.New(guestd.Config{
		System: system,
		RunRunner: func(_ context.Context, jitConfig string, _ map[string]string, event func(guestd.RunnerEvent)) (int, error) {
			ran <- jitConfig
			event(guestd.EventListening)
			assignment := guestproto.Assignment{
				RequestID: "request-loop", JobID: "job-loop", RunnerName: "lease-loop",
				JobDisplayName: "test", Identity: &identity,
			}
			if err := guestd.AwaitRunnerAssignment(context.Background(), assignmentSocket, assignment); err != nil {
				return 0, err
			}
			if _, err := guestd.ValidateRunnerAssignment(context.Background(), assignmentSocket, identity); err != nil {
				return 0, err
			}
			return 0, nil
		},
		RetryInterval: time.Millisecond,
		HostCID:       vsock.Local,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(serveCtx, listener)
	}()
	assignmentServed := make(chan struct{})
	go func() {
		defer close(assignmentServed)
		_ = server.ServeAssignments(serveCtx, assignmentSocket)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
		<-assignmentServed
	})

	transport := NewVsockGuest()
	transport.port = port
	const id = ID("vm-loop")

	waitFor(t, "hello over loopback", 10*time.Second, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		observed, err := transport.Observe(ctx, id, vsock.Local)
		if err != nil {
			return false, err
		}
		return observed.Hello, nil
	})

	deliverCtx, deliverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer deliverCancel()
	prepare := guestproto.Prepare{Lease: "lease-loop", JITConfig: "jit-blob"}
	if err := transport.Prepare(deliverCtx, id, vsock.Local, prepare); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	waitFor(t, "local assignment over loopback", 10*time.Second, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		observed, err := transport.Observe(ctx, id, vsock.Local)
		return observed.Assignment != nil && observed.Assignment.RequestID == "request-loop", err
	})
	rendezvous := guestproto.Rendezvous{
		Lease:  "lease-loop",
		Mounts: []guestproto.Mount{{Serial: "workspace", Filesystem: "ext4", Mountpoint: "/work", Options: []string{"discard"}}},
	}
	if err := transport.Rendezvous(deliverCtx, id, vsock.Local, rendezvous); err != nil {
		t.Fatalf("rendezvous: %v", err)
	}
	waitFor(t, "generation restore over loopback", 10*time.Second, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		observed, err := transport.Observe(ctx, id, vsock.Local)
		return observed.MountsReady, err
	})
	if err := transport.Authorize(deliverCtx, id, vsock.Local, guestproto.Authorize{
		Lease: "lease-loop", RequestID: "request-loop", Identity: &identity,
	}); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	waitFor(t, "runner exit over loopback", 10*time.Second, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		observed, err := transport.Observe(ctx, id, vsock.Local)
		if err != nil {
			return false, err
		}
		return observed.Released && observed.RunnerExited && observed.ExitCode == 0, nil
	})
	if got := <-ran; got != "jit-blob" {
		t.Fatalf("runner ran with jit %q", got)
	}
	if mounted, _ := system.IsMounted("/work"); !mounted {
		t.Fatal("workspace never mounted")
	}

	quiesceCtx, quiesceCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer quiesceCancel()
	if _, err := transport.Quiesce(quiesceCtx, id, vsock.Local, guestproto.Quiesce{Mountpoints: []string{"/work"}}); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if mounted, _ := system.IsMounted("/work"); !mounted {
		t.Fatal("workspace was unmounted before VM teardown")
	}
}
