package parkproxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestProxyRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	want := Open{Sub: "alice", Park: "park-mythra", Role: "player", Remote: "127.0.0.1:1", SinceSeq: 41, SinceTick: 90}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		proxy, got, err := Accept(conn, key, time.Now())
		if err == nil && got != want {
			t.Errorf("open = %+v, want %+v", got, want)
		}
		if err == nil {
			kind, payload, readErr := proxy.ReadMessage()
			if readErr != nil {
				err = readErr
			} else if kind != KindStream || string(payload) != "hello" {
				t.Errorf("message = %d %q", kind, payload)
			}
		}
		done <- err
	}()

	proxy, err := Dial(context.Background(), listener.Addr().String(), key, want)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if err := proxy.WriteMessage(KindStream, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyRejectsWrongKey(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	done := make(chan error, 1)
	go func() {
		_, _, err := Accept(right, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), time.Now())
		done <- err
	}()
	p := &Conn{Conn: left}
	if err := p.writeOpen([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Open{Sub: "a", Park: "p", Role: "player"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("wrong key accepted")
	}
}
