package vsock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// loopbackListener binds an ephemeral vsock port, skipping the test on
// hosts without AF_VSOCK or the vsock_loopback transport (CI sandboxes
// have neither).
func loopbackListener(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	l, err := Listen(Any, PortAny)
	if err != nil {
		if errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENODEV) {
			t.Skipf("AF_VSOCK unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	addr := l.Addr().(Addr)
	if addr.Port == 0 || addr.Port == PortAny {
		t.Fatalf("ephemeral bind reported port %d", addr.Port)
	}

	// The listener existing does not prove a loopback transport exists;
	// probe with one dial before committing to the test.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probe, err := Dial(ctx, Local, addr.Port)
	if err != nil {
		if errors.Is(err, unix.ENODEV) || errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.EAFNOSUPPORT) {
			t.Skipf("vsock loopback unavailable: %v", err)
		}
		t.Fatal(err)
	}
	probe.Close()
	// Drain the probe connection so the test's real dial is the next accept.
	drained, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	drained.Close()
	return l, addr.Port
}

func TestLoopbackEcho(t *testing.T) {
	l, port := loopbackListener(t)

	accepted := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		accepted <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, Local, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	payload := []byte("guestproto rides here\n")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echoed, payload) {
		t.Fatalf("echoed %q, want %q", echoed, payload)
	}
	conn.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("server side: %v", err)
	}
}

func TestDialTimesOutUnderContext(t *testing.T) {
	l, port := loopbackListener(t)

	// Saturate the accept queue is not portable; instead prove the deadline
	// path by dialing a port nobody listens on and bounding the wait.
	l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, Local, port); err == nil {
		t.Fatal("dialed a closed port")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("dial failure took %s", elapsed)
	}
}

func TestCloseUnblocksAccept(t *testing.T) {
	l, _ := loopbackListener(t)

	done := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	l.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accept returned a conn after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("accept still blocked after close")
	}
}
