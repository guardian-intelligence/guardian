package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHubCoalescesToLatest(t *testing.T) {
	h := newHub()
	ch, current, cancel := h.subscribe()
	defer cancel()
	if current != "" {
		t.Fatalf("expected empty initial epoch, got %q", current)
	}
	h.set("aaa")
	h.set("bbb")
	h.set("ccc")
	select {
	case got := <-ch:
		if got != "ccc" {
			t.Fatalf("expected latest epoch ccc, got %q", got)
		}
	default:
		t.Fatal("no epoch delivered")
	}
	select {
	case got := <-ch:
		t.Fatalf("expected coalesced single delivery, got extra %q", got)
	default:
	}
}

func TestSSEStreamsInitialAndChangedEpoch(t *testing.T) {
	h := newHub()
	h.set("e1e1e1")

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/features/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.serveSSE(rec, req)
		close(done)
	}()

	waitFor := func(marker string) {
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(rec.Body.String(), marker) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in %q", marker, rec.Body.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("event: epoch\ndata: e1e1e1\n\n")
	h.set("f2f2f2")
	waitFor("event: epoch\ndata: f2f2f2\n\n")

	cancel()
	<-done
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
}
