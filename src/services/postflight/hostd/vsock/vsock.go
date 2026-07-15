// Package vsock is a minimal AF_VSOCK dial/listen surface for the
// hostd↔guestd channel: stream sockets wrapped as net.Conn/net.Listener,
// nothing else. The host dials a VM's CID; guestd listens on every CID.
package vsock

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// Host is the CID every host-originated connection bears inside a guest.
	Host = unix.VMADDR_CID_HOST
	// Local is the loopback CID served by the vsock_loopback transport.
	Local = unix.VMADDR_CID_LOCAL
	// Any binds every CID on listen.
	Any = unix.VMADDR_CID_ANY
	// PortAny requests an ephemeral port on listen; read the assignment
	// back from Listener.Addr.
	PortAny = unix.VMADDR_PORT_ANY
)

// Addr is a vsock endpoint.
type Addr struct {
	CID  uint32
	Port uint32
}

// Network implements net.Addr.
func (Addr) Network() string { return "vsock" }

// String implements net.Addr.
func (a Addr) String() string { return fmt.Sprintf("vsock:%d:%d", a.CID, a.Port) }

// conn adapts a connected vsock socket to net.Conn. The fd is non-blocking
// and registered with the runtime poller via os.NewFile, which is what makes
// deadlines and concurrent Close work.
type conn struct {
	file   *os.File
	local  Addr
	remote Addr
}

func (c *conn) Read(b []byte) (int, error)         { return c.file.Read(b) }
func (c *conn) Write(b []byte) (int, error)        { return c.file.Write(b) }
func (c *conn) Close() error                       { return c.file.Close() }
func (c *conn) LocalAddr() net.Addr                { return c.local }
func (c *conn) RemoteAddr() net.Addr               { return c.remote }
func (c *conn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

func socketFile() (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock: socket: %w", err)
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}

func sockname(file *os.File, get func(fd int) (unix.Sockaddr, error)) Addr {
	var addr Addr
	raw, err := file.SyscallConn()
	if err != nil {
		return addr
	}
	_ = raw.Control(func(fd uintptr) {
		if sa, err := get(int(fd)); err == nil {
			if vm, ok := sa.(*unix.SockaddrVM); ok {
				addr = Addr{CID: vm.CID, Port: vm.Port}
			}
		}
	})
	return addr
}

// Dial connects to a vsock peer. The context bounds the connect; a peer
// that is not up yet (a guest still booting) fails here and the caller's
// retry policy owns what happens next.
func Dial(ctx context.Context, cid, port uint32) (net.Conn, error) {
	file, err := socketFile()
	if err != nil {
		return nil, err
	}
	sa := &unix.SockaddrVM{CID: cid, Port: port}
	if err := connect(ctx, file, sa); err != nil {
		file.Close()
		return nil, fmt.Errorf("vsock: connect cid %d port %d: %w", cid, port, err)
	}
	return &conn{
		file:   file,
		local:  sockname(file, unix.Getsockname),
		remote: Addr{CID: cid, Port: port},
	}, nil
}

// connect drives a non-blocking connect to completion under the context.
// The first attempt returns EINPROGRESS; the poller then wakes the write
// closure when the handshake resolves, and re-issuing connect disambiguates
// success (EISCONN) from failure.
func connect(ctx context.Context, file *os.File, sa *unix.SockaddrVM) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := file.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = file.SetWriteDeadline(time.Now()) })
	defer stop()

	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var connErr error
	waitErr := raw.Write(func(fd uintptr) bool {
		switch err := unix.Connect(int(fd), sa); err {
		case nil, unix.EISCONN:
			connErr = nil
			return true
		case unix.EINPROGRESS, unix.EALREADY, unix.EINTR:
			return false
		default:
			connErr = err
			return true
		}
	})
	if waitErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return waitErr
	}
	if connErr != nil {
		return connErr
	}
	return file.SetWriteDeadline(time.Time{})
}

// listener adapts a listening vsock socket to net.Listener.
type listener struct {
	file *os.File
	addr Addr
}

// Listen binds and listens on a vsock port. cid is usually Any; pass
// PortAny for an ephemeral port and read it back from Addr.
func Listen(cid, port uint32) (net.Listener, error) {
	file, err := socketFile()
	if err != nil {
		return nil, err
	}
	raw, err := file.SyscallConn()
	if err != nil {
		file.Close()
		return nil, err
	}
	var bindErr error
	if err := raw.Control(func(fd uintptr) {
		if bindErr = unix.Bind(int(fd), &unix.SockaddrVM{CID: cid, Port: port}); bindErr != nil {
			return
		}
		bindErr = unix.Listen(int(fd), unix.SOMAXCONN)
	}); err != nil {
		bindErr = err
	}
	if bindErr != nil {
		file.Close()
		return nil, fmt.Errorf("vsock: listen cid %d port %d: %w", cid, port, bindErr)
	}
	return &listener{file: file, addr: sockname(file, unix.Getsockname)}, nil
}

// Accept implements net.Listener.
func (l *listener) Accept() (net.Conn, error) {
	raw, err := l.file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		acceptedFD int
		remote     Addr
		acceptErr  error
	)
	waitErr := raw.Read(func(fd uintptr) bool {
		nfd, sa, err := unix.Accept4(int(fd), unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		switch err {
		case nil:
			acceptedFD = nfd
			if vm, ok := sa.(*unix.SockaddrVM); ok {
				remote = Addr{CID: vm.CID, Port: vm.Port}
			}
			return true
		case unix.EAGAIN, unix.EINTR, unix.ECONNABORTED:
			// ECONNABORTED: the peer gave up mid-handshake — retry, it must
			// not surface as a listener failure.
			return false
		default:
			acceptErr = err
			return true
		}
	})
	if waitErr != nil {
		return nil, fmt.Errorf("vsock: accept: %w", waitErr)
	}
	if acceptErr != nil {
		return nil, fmt.Errorf("vsock: accept: %w", acceptErr)
	}
	return &conn{
		file:   os.NewFile(uintptr(acceptedFD), "vsock"),
		local:  l.addr,
		remote: remote,
	}, nil
}

// Close implements net.Listener; it unblocks a pending Accept.
func (l *listener) Close() error { return l.file.Close() }

// Addr implements net.Listener.
func (l *listener) Addr() net.Addr { return l.addr }
