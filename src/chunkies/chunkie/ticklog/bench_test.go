package ticklog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// BenchmarkAppendTick measures the tick thread's whole cost: encode into
// a pooled buffer, checksum, and hand off. This is the number the flip
// buys — it replaces a blocking Postgres round trip on the hot path.
func BenchmarkAppendTick(b *testing.B) {
	l, err := Create(Config{Dir: b.TempDir(), Generation: 1, Chunks: specsFor(1),
		MaxDurableLag: time.Hour, QueueDepth: 1 << 15})
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	payload := run(7)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		if err := l.AppendTick(0, uint64(i), int64(i), 1, payload, uint64(i)); err != nil {
			b.Fatal(err)
		}
		if i%(1<<14) == 0 {
			// Off the clock: let the syncer drain so the queue never
			// backpressures the measurement.
			b.StopTimer()
			if err := l.Barrier(ctx); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
		}
	}
}

// TestSoakSustainedRate holds the engine at an order of magnitude past
// WUM's measured event rate across several chunks and requires zero
// faults, full durability, and a verifying replay.
func TestSoakSustainedRate(t *testing.T) {
	if testing.Short() {
		t.Skip("soak")
	}
	const chunks, ticks = 8, 5000
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(chunks), SegmentBytes: 1 << 22})
	if err != nil {
		t.Fatal(err)
	}
	payload := run(3)
	for tick := 1; tick <= ticks; tick++ {
		for c := 0; c < chunks; c++ {
			if err := l.AppendTick(c, uint64(tick), int64(tick), 1, payload, uint64(tick)); err != nil {
				t.Fatalf("tick %d chunk %d: %v", tick, c, err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-l.Faults():
		t.Fatalf("soak faulted: %v", f)
	default:
	}
	for c := 0; c < chunks; c++ {
		if d := l.Durable(c); d.Tick != ticks || d.Seq != ticks {
			t.Fatalf("chunk %d durable %+v, want tick %d", c, d, ticks)
		}
	}
	counts := map[string]int{}
	if _, err := Scan(dir, keysFor(chunks), func(chunk string, r codec.Record) error {
		counts[chunk]++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < chunks; c++ {
		if n := counts[fmt.Sprintf("chunk-%d", c)]; n != ticks {
			t.Fatalf("chunk %d replayed %d records, want %d", c, n, ticks)
		}
	}
}
