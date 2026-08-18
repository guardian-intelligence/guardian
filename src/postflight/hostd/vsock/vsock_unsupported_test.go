//go:build !linux

package vsock

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedTransportFailsClosed(t *testing.T) {
	if conn, err := Dial(context.Background(), Host, 8480); conn != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Dial() = (%v, %v), want nil and ErrUnsupported", conn, err)
	}
	if listener, err := Listen(Any, 8480); listener != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Listen() = (%v, %v), want nil and ErrUnsupported", listener, err)
	}
}
