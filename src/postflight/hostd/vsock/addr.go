// Package vsock is a minimal AF_VSOCK dial/listen surface for the
// hostd↔guestd channel: stream sockets wrapped as net.Conn/net.Listener,
// nothing else. The host dials a VM's CID; guestd listens on every CID.
package vsock

import (
	"errors"
	"fmt"
)

const (
	// Host is the CID every host-originated connection bears inside a guest.
	Host uint32 = 2
	// Local is the loopback CID served by the Linux vsock_loopback transport.
	Local uint32 = 1
	// Any binds every CID on listen.
	Any uint32 = 0xffffffff
	// PortAny requests an ephemeral port on listen; read the assignment
	// back from Listener.Addr.
	PortAny uint32 = 0xffffffff
)

// ErrUnsupported reports that the current operating system cannot provide
// the Linux AF_VSOCK transport. Callers must fail closed or use a test fake;
// there is no network fallback for the host↔guest trust boundary.
var ErrUnsupported = errors.New("vsock: transport requires Linux")

// Addr is a vsock endpoint.
type Addr struct {
	CID  uint32
	Port uint32
}

// Network implements net.Addr.
func (Addr) Network() string { return "vsock" }

// String implements net.Addr.
func (a Addr) String() string { return fmt.Sprintf("vsock:%d:%d", a.CID, a.Port) }
