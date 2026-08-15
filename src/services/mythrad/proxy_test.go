package main

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

	want := proxyOpen{Sub: "alice", Park: "park-mythra", Role: "player", Remote: "127.0.0.1:1", SinceSeq: 41, SinceTick: 90}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		proxy, got, err := acceptProxy(conn, key, time.Now())
		if err == nil && got != want {
			t.Errorf("open = %+v, want %+v", got, want)
		}
		if err == nil {
			kind, payload, readErr := proxy.readMessage()
			if readErr != nil {
				err = readErr
			} else if kind != proxyStream || string(payload) != "hello" {
				t.Errorf("message = %d %q", kind, payload)
			}
		}
		done <- err
	}()

	proxy, err := dialProxy(context.Background(), listener.Addr().String(), key, want)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	if err := proxy.writeMessage(proxyStream, []byte("hello")); err != nil {
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
		_, _, err := acceptProxy(right, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), time.Now())
		done <- err
	}()
	p := &proxyConn{Conn: left}
	if err := p.writeOpen([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), proxyOpen{Sub: "a", Park: "p", Role: "player"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("wrong key accepted")
	}
}
