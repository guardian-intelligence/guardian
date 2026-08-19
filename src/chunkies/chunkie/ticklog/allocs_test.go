//go:build !race

// The allocation gate measures the real hot path; race instrumentation
// allocates on its own and would fail it spuriously.

package ticklog

import (
	"context"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// TestAppendTickStaysAllocationFree is the regression gate on the hot
// path: past warmup, appends at tick-loop pacing must amortize to zero
// allocations. Pacing matters — buffers recycle through the syncer, so
// an unbounded burst measures pool misses no real tick loop can create.
func TestAppendTickStaysAllocationFree(t *testing.T) {
	restore := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(restore)
	l, err := Create(Config{Dir: t.TempDir(), Generation: 1, Chunks: specsFor(1), MaxDurableLag: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	payload := run(7)
	tick := uint64(0)
	seq := int64(0)
	burst := func(n int) {
		for i := 0; i < n; i++ {
			tick++
			seq++
			if err := l.AppendTick(0, tick, seq, 1, payload, tick); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.Barrier(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	burst(500) // warmup: pool, write buffer, and queue reach steady state

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const rounds, perRound = 20, 100
	for i := 0; i < rounds; i++ {
		burst(perRound)
	}
	runtime.ReadMemStats(&after)
	perAppend := float64(after.Mallocs-before.Mallocs) / (rounds * perRound)
	if perAppend >= 0.5 {
		t.Fatalf("AppendTick allocates %.2f times per call at steady state", perAppend)
	}
}
