//go:build !linux

package vsock

import (
	"context"
	"fmt"
	"net"
	"runtime"
)

// Dial fails closed on platforms without the Linux AF_VSOCK transport.
func Dial(_ context.Context, cid, port uint32) (net.Conn, error) {
	return nil, fmt.Errorf("vsock: connect cid %d port %d on %s: %w", cid, port, runtime.GOOS, ErrUnsupported)
}

// Listen fails closed on platforms without the Linux AF_VSOCK transport.
func Listen(cid, port uint32) (net.Listener, error) {
	return nil, fmt.Errorf("vsock: listen cid %d port %d on %s: %w", cid, port, runtime.GOOS, ErrUnsupported)
}
