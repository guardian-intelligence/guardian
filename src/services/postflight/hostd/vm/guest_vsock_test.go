package vm

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
)

// pipeDialer swaps the AF_VSOCK dial for an in-memory pipe served by a
// scripted guest, so every transport behavior is testable without a kernel.
func pipeDialer(serve func(conn net.Conn)) (*VsockGuest, *atomic.Int32) {
	dials := &atomic.Int32{}
	transport := NewVsockGuest()
	transport.dial = func(_ context.Context, _, _ uint32) (net.Conn, error) {
		dials.Add(1)
		host, guest := net.Pipe()
		go serve(guest)
		return host, nil
	}
	return transport, dials
}

func sendHello(t *testing.T, conn net.Conn) {
	t.Helper()
	encoder := guestproto.NewEncoder(conn)
	if err := encoder.Write(guestproto.Message{Kind: guestproto.KindHello, Hello: &guestproto.Hello{Version: guestproto.Version}}); err != nil {
		t.Errorf("guest hello: %v", err)
	}
}

func observation(t *testing.T, transport *VsockGuest, id ID, cid uint32) GuestObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	observed, err := transport.Observe(ctx, id, cid)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return observed
}

func TestVsockGuestProbeIsHelloWithinDeadline(t *testing.T) {
	transport, _ := pipeDialer(func(conn net.Conn) {
		sendHello(t, conn)
		encoder := guestproto.NewEncoder(conn)
		_ = encoder.Write(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &guestproto.RunnerStatus{State: guestproto.RunnerRegistered}})
		_ = encoder.Write(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &guestproto.RunnerStatus{
			State:    guestproto.RunnerHookBlocked,
			Identity: &guestproto.JobIdentity{RunID: "1", RunAttempt: 1, RunnerName: "r", Repository: "acme/widget"},
		}})
		_ = encoder.Write(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &guestproto.RunnerStatus{
			State: guestproto.RunnerReleased,
			Clock: &guestproto.ClockSample{UnixNS: 1, Clocksource: "kvm-clock"},
		}})
		_ = encoder.Write(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: 7}})
	})
	if got := observation(t, transport, "vm-a", 3); !got.Hello {
		t.Fatalf("observation %+v after hello", got)
	}
	waitFor(t, "runner statuses folded", 2*time.Second, func() (bool, error) {
		got := observation(t, transport, "vm-a", 3)
		return got.RunnerRegistered && got.RunnerExited && got.ExitCode == 7, nil
	})
}

func TestVsockGuestRejectsOldProtocol(t *testing.T) {
	transport, _ := pipeDialer(func(conn net.Conn) {
		defer conn.Close()
		_ = guestproto.NewEncoder(conn).Write(guestproto.Message{
			Kind: guestproto.KindHello,
			Hello: &guestproto.Hello{
				Version: guestproto.Version - 1,
			},
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := transport.Prepare(ctx, "vm-old", 3, guestproto.Prepare{Lease: "l1"}); err == nil ||
		!strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("old protocol prepare error = %v", err)
	}
}

func TestVsockGuestDialFailureIsTheZeroObservation(t *testing.T) {
	transport := NewVsockGuest()
	transport.dial = func(context.Context, uint32, uint32) (net.Conn, error) {
		return nil, errors.New("connect: no such device")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observed, err := transport.Observe(ctx, "vm-a", 3)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observed.Hello || observed.Assignment != nil || observed.RunnerRegistered || observed.RunnerExited || len(observed.Timing) != 0 {
		t.Fatalf("observation %+v, want zero", observed)
	}

	// Prepare, by contrast, must surface the failure.
	if err := transport.Prepare(ctx, "vm-a", 3, guestproto.Prepare{Lease: "l1"}); err == nil {
		t.Fatal("delivered over a dead channel")
	}
}

func TestVsockGuestSilentGuestObservesZeroWithinDeadline(t *testing.T) {
	transport, _ := pipeDialer(func(conn net.Conn) {
		// Accepts and says nothing — a guest that is not guestd yet.
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	observed, err := transport.Observe(ctx, "vm-a", 3)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observed.Hello {
		t.Fatal("hello from a silent guest")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("observe took %s against a silent guest", elapsed)
	}
}

func TestVsockGuestPrepareWritesTheListener(t *testing.T) {
	received := make(chan guestproto.Prepare, 1)
	transport, _ := pipeDialer(func(conn net.Conn) {
		sendHello(t, conn)
		decoder := guestproto.NewDecoder(conn)
		message, err := decoder.Read()
		if err != nil {
			t.Errorf("guest read: %v", err)
			return
		}
		if message.Kind != guestproto.KindPrepare {
			t.Errorf("guest got %s, want prepare", message.Kind)
			return
		}
		received <- *message.Prepare
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	prepare := guestproto.Prepare{Lease: "lease-1", JITConfig: "jit-blob"}
	if err := transport.Prepare(ctx, "vm-a", 3, prepare); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	select {
	case got := <-received:
		if got.Lease != prepare.Lease || got.JITConfig != "jit-blob" {
			t.Fatalf("guest received %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("preparation never reached the guest")
	}
}

func TestVsockGuestQuiesceRoundTrip(t *testing.T) {
	replies := map[string]guestproto.Message{
		"ok": {Kind: guestproto.KindQuiesced, Quiesced: &guestproto.Quiesced{Checkpoint: &guestproto.CheckpointArtifact{
			Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Version: "Version: 4.2",
		}}},
		"failed": {Kind: guestproto.KindQuiesceFailed, QuiesceFailed: &guestproto.QuiesceFailed{Reason: "target is busy"}},
	}
	for name, reply := range replies {
		t.Run(name, func(t *testing.T) {
			transport, _ := pipeDialer(func(conn net.Conn) {
				sendHello(t, conn)
				decoder := guestproto.NewDecoder(conn)
				message, err := decoder.Read()
				if err != nil || message.Kind != guestproto.KindQuiesce {
					t.Errorf("guest got %v (err %v), want quiesce", message.Kind, err)
					return
				}
				if len(message.Quiesce.Mountpoints) != 1 || message.Quiesce.Mountpoints[0] != "/work" {
					t.Errorf("quiesce mountpoints %q", message.Quiesce.Mountpoints)
				}
				_ = guestproto.NewEncoder(conn).Write(reply)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := transport.Quiesce(ctx, "vm-a", 3, guestproto.Quiesce{
				Mountpoints: []string{"/work"},
				Checkpoint:  &guestproto.CheckpointDump{ImagesDir: "/process/images", ExternalMountAt: "/work"},
			})
			if name == "ok" && err != nil {
				t.Fatalf("quiesce: %v", err)
			}
			if name == "ok" && (got.Checkpoint == nil || got.Checkpoint.Version != "Version: 4.2") {
				t.Fatalf("quiesce reply %+v", got)
			}
			if name == "failed" {
				if err == nil || !strings.Contains(err.Error(), "target is busy") {
					t.Fatalf("quiesce error %v, want the guest's reason", err)
				}
			}
		})
	}
}

func TestVsockGuestRedialsAfterConnectionLoss(t *testing.T) {
	transport, dials := pipeDialer(func(conn net.Conn) {
		sendHello(t, conn)
		conn.Close()
	})
	if got := observation(t, transport, "vm-a", 3); !got.Hello {
		t.Fatalf("observation %+v after first hello", got)
	}
	// The connection died; the channel retires itself and the next Observe
	// dials fresh instead of reporting stale state forever.
	waitFor(t, "channel retirement and redial", 2*time.Second, func() (bool, error) {
		got := observation(t, transport, "vm-a", 3)
		return got.Hello && dials.Load() >= 2, nil
	})
}

func TestVsockGuestNewCIDSupersedesTheChannel(t *testing.T) {
	transport := NewVsockGuest()
	var dials atomic.Int32
	transport.dial = func(context.Context, uint32, uint32) (net.Conn, error) {
		life := dials.Add(1)
		host, guest := net.Pipe()
		go func() {
			sendHello(t, guest)
			if life == 1 {
				_ = guestproto.NewEncoder(guest).Write(guestproto.Message{Kind: guestproto.KindRunnerStatus, RunnerStatus: &guestproto.RunnerStatus{State: guestproto.RunnerExited, ExitCode: 9}})
			}
			// Hold the connection open; only supersession retires it.
			buf := make([]byte, 1)
			_, _ = guest.Read(buf)
		}()
		return host, nil
	}
	waitFor(t, "first life's exit", 2*time.Second, func() (bool, error) {
		got := observation(t, transport, "vm-a", 3)
		return got.RunnerExited && got.ExitCode == 9, nil
	})
	// Same ID relaunched under a new CID: the observation starts over —
	// the old life's exit must not leak into the new one.
	if got := observation(t, transport, "vm-a", 4); got.RunnerExited {
		t.Fatalf("observation %+v inherited the previous life", got)
	}
	if dials.Load() < 2 {
		t.Fatalf("dialed %d times, want a fresh dial for the new cid", dials.Load())
	}
}
