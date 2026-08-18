package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type flakySink struct {
	mu       sync.Mutex
	fail     bool
	inserted [][]eventRow
}

func (f *flakySink) Insert(_ context.Context, rows []eventRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("sink down")
	}
	f.inserted = append(f.inserted, append([]eventRow(nil), rows...))
	return nil
}

func (f *flakySink) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.inserted {
		n += len(b)
	}
	return n
}

func rowsN(n int, seqStart uint32) []eventRow {
	out := make([]eventRow, n)
	for i := range out {
		out[i] = eventRow{EventName: "page_view", SessionSeq: seqStart + uint32(i)}
	}
	return out
}

func TestBatcherFlushesOnSize(t *testing.T) {
	sink := &flakySink{}
	b := newBatcher(sink, 10, time.Hour, 1000)
	defer b.Close()
	b.Add(rowsN(10, 0))
	deadline := time.Now().Add(3 * time.Second)
	for sink.total() < 10 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sink.total() != 10 {
		t.Fatalf("flushed %d rows, want 10", sink.total())
	}
}

func TestBatcherCloseFlushesRemainder(t *testing.T) {
	sink := &flakySink{}
	b := newBatcher(sink, 1000, time.Hour, 1000)
	b.Add(rowsN(3, 0))
	b.Close()
	if sink.total() != 3 {
		t.Fatalf("flushed %d rows on close, want 3", sink.total())
	}
}

func TestBatcherDropsOldestOverCap(t *testing.T) {
	sink := &flakySink{fail: true}
	b := newBatcher(sink, 1_000_000, time.Hour, 50)
	defer b.Close()
	b.Add(rowsN(40, 0))
	b.Add(rowsN(40, 1000))
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	b.mu.Lock()
	kept := append([]eventRow(nil), b.rows...)
	b.mu.Unlock()
	if len(kept) != 50 {
		t.Fatalf("buffer holds %d rows, want cap 50", len(kept))
	}
	// Oldest dropped: the head must now be from the tail of the first batch.
	if kept[0].SessionSeq != 30 {
		t.Fatalf("head seq = %d, want 30 (drop-oldest)", kept[0].SessionSeq)
	}
}

func TestBatcherRequeuesOnFailure(t *testing.T) {
	sink := &flakySink{fail: true}
	b := newBatcher(sink, 5, 50*time.Millisecond, 1000)
	defer b.Close()
	b.Add(rowsN(5, 0))
	time.Sleep(300 * time.Millisecond) // at least one failed flush
	sink.mu.Lock()
	sink.fail = false
	sink.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for sink.total() < 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.total() != 5 {
		t.Fatalf("recovered flush total = %d, want 5 (no loss under transient outage)", sink.total())
	}
}
