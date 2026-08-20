package gateway

import (
	"io"
	"net"
	"testing"
	"time"
)

// The TCP twin's contract: a connection to the WT port's TCP side resolves
// immediately — accepted and closed — so a fallback racer never waits.
func TestRefuseTCPClosesImmediately(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go refuseTCP(ln)
	defer ln.Close()

	for i := 0; i < 3; i++ {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1)
		_, err = conn.Read(buf)
		if err != io.EOF {
			t.Fatalf("dial %d: want immediate EOF, got %v", i, err)
		}
		conn.Close()
	}
}
