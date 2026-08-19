package ticklog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFailedSyncPoisonsForever is the fsyncgate rule: after one failed
// sync the engine must fault, stop accepting, and never write that file
// again — dirty pages may be gone, so a later clean sync proves nothing.
func TestFailedSyncPoisonsForever(t *testing.T) {
	fs := newMemfs()
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Hour, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	fs.failSyncAfter = 1 // the next sync — the barrier's — fails
	fs.mu.Unlock()
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Barrier(context.Background()); err == nil {
		t.Fatal("barrier over a failed sync reported success")
	}
	select {
	case <-l.Faults():
	case <-time.After(2 * time.Second):
		t.Fatal("no fault delivered")
	}
	if err := l.AppendTick(0, 2, 2, 1, run(2), 2); err == nil {
		t.Fatal("append accepted after poison")
	}
	if d := l.Durable(0); d.Seq != 0 {
		t.Fatalf("durable advanced past a failed sync: %+v", d)
	}
	l.Close()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	failed := -1
	for i, op := range fs.ops {
		if strings.HasPrefix(op, "sync-FAIL") {
			failed = i
		}
		if failed >= 0 && i > failed && strings.HasPrefix(op, "write") {
			t.Fatalf("write after failed sync: %v", fs.ops[failed:])
		}
	}
	if failed < 0 {
		t.Fatal("injected sync failure never happened")
	}
}

// TestFullQueueStallsInsteadOfDropping: when the syncer cannot drain, the
// log declares itself stalled rather than silently widening the loss
// window.
func TestFullQueueStalls(t *testing.T) {
	fs := newMemfs()
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Millisecond, QueueDepth: 2, MaxDurableLag: time.Hour, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.setStall(true)
	var stallErr error
	for i := 1; i <= 64; i++ {
		if stallErr = l.AppendTick(0, uint64(i), int64(i), 1, run(i), 0); stallErr != nil {
			break
		}
	}
	if !errors.Is(stallErr, ErrStalled) {
		t.Fatalf("append over a full queue: %v, want ErrStalled", stallErr)
	}
	select {
	case f := <-l.Faults():
		if !errors.Is(f, ErrStalled) {
			t.Fatalf("fault %v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no fault delivered")
	}
	fs.setStall(false)
	l.Close()
}

// TestWatchdogTripsOnHungSync: a write hung inside the kernel can't be
// noticed by the syncer itself; the watchdog must fence when the oldest
// unsynced record outlives MaxDurableLag.
func TestWatchdogTripsOnHungSync(t *testing.T) {
	fs := newMemfs()
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Millisecond, MaxDurableLag: 30 * time.Millisecond, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.setStall(true)
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-l.Faults():
		if !errors.Is(f, ErrStalled) {
			t.Fatalf("fault %v", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never tripped")
	}
	if lag := l.DurableLag(); lag < 30*time.Millisecond {
		t.Fatalf("lag %v under the ceiling at fault time", lag)
	}
	fs.setStall(false)
	l.Close()
}

// TestCloseReturnsOnHungDisk: with the syncer stuck inside a hung write
// forever, Close must return the fault instead of hanging — the syncer
// goroutine is unreclaimable, the caller is not.
func TestCloseReturnsOnHungDisk(t *testing.T) {
	fs := newMemfs()
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Millisecond, MaxDurableLag: 30 * time.Millisecond, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.setStall(true) // never cleared
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Faults():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never tripped")
	}
	closed := make(chan error, 1)
	go func() { closed <- l.Close() }()
	select {
	case err := <-closed:
		if !errors.Is(err, ErrStalled) {
			t.Fatalf("close on a hung disk: %v, want the stall fault", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung on a hung disk")
	}
}

// TestBarrierRespectsContext: a barrier against a wedged log must return
// with the context, not hang.
func TestBarrierRespectsContext(t *testing.T) {
	fs := newMemfs()
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Millisecond, MaxDurableLag: time.Hour, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.setStall(true)
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.Barrier(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("barrier: %v, want deadline exceeded", err)
	}
	fs.setStall(false)
	l.Close()
}
