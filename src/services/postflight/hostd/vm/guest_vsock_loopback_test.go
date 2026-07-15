package vm

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	server, err := guestd.New(guestd.Config{
		System: system,
		RunRunner: func(_ context.Context, jitConfig string, _ map[string]string, event func(guestd.RunnerEvent)) (int, error) {
			ran <- jitConfig
			event(guestd.EventListening)
			event(guestd.EventJobStarted)
			return 0, nil
		},
		RetryInterval: time.Millisecond,
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
	t.Cleanup(func() {
		cancel()
		<-served
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
	assignment := guestproto.Assignment{
		Lease:     "lease-loop",
		Mounts:    []guestproto.Mount{{Serial: "workspace", Filesystem: "ext4", Mountpoint: "/work", Options: []string{"discard"}}},
		JITConfig: "jit-blob",
	}
	if err := transport.Deliver(deliverCtx, id, vsock.Local, assignment); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	waitFor(t, "runner exit over loopback", 10*time.Second, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		observed, err := transport.Observe(ctx, id, vsock.Local)
		if err != nil {
			return false, err
		}
		return observed.RunnerRegistered && observed.RunnerExited && observed.ExitCode == 0, nil
	})
	if got := <-ran; got != "jit-blob" {
		t.Fatalf("runner ran with jit %q", got)
	}
	if mounted, _ := system.IsMounted("/work"); !mounted {
		t.Fatal("workspace never mounted")
	}

	quiesceCtx, quiesceCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer quiesceCancel()
	if err := transport.Quiesce(quiesceCtx, id, vsock.Local, "/work"); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if mounted, _ := system.IsMounted("/work"); mounted {
		t.Fatal("workspace still mounted after quiesce")
	}
}
