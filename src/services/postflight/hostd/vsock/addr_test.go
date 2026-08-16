package vsock

import "testing"

func TestAddr(t *testing.T) {
	addr := Addr{CID: Host, Port: 8480}
	if got, want := addr.Network(), "vsock"; got != want {
		t.Fatalf("network %q, want %q", got, want)
	}
	if got, want := addr.String(), "vsock:2:8480"; got != want {
		t.Fatalf("string %q, want %q", got, want)
	}
}
