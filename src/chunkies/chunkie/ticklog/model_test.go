package ticklog

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// TestModel drives random interleavings of appends, watermarks, barriers,
// and epoch advances against an in-memory model, then crashes the log at
// a random point and checks both endpoints of what a real crash can
// leave: only-synced bytes, and synced plus everything written since.
// Recovery must always yield a per-chunk prefix of the model containing
// at least everything a barrier acknowledged — and a successor generation
// must resume into one dense history.
func TestModel(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			testModelSeed(t, seed)
		})
	}
}

func testModelSeed(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	fs := newMemfs()
	const chunks = 2
	l, err := Create(Config{
		Dir: "/log", Generation: 1, Chunks: specsFor(chunks),
		SyncInterval: time.Millisecond, SegmentBytes: 1 << 10, fs: fs,
	})
	if err != nil {
		t.Fatal(err)
	}

	model := make([][]appended, chunks) // per chunk, in order
	acked := make([]Tip, chunks)        // frontier the last barrier guaranteed
	ticks := make([]uint64, chunks)
	seqs := make([]int64, chunks)
	epoch := uint32(1)

	ops := 200 + rng.Intn(200)
	for i := 0; i < ops; i++ {
		c := rng.Intn(chunks)
		switch r := rng.Float64(); {
		case r < 0.70:
			ticks[c] += 1 + uint64(rng.Intn(3))
			count := uint16(1 + rng.Intn(3))
			var runBytes []byte
			for e := 0; e < int(count); e++ {
				runBytes = codec.AppendEventRecord(runBytes, uint64(i*8+e+1), 7, uint64(c+1), []byte{byte(i), byte(e)})
			}
			a := appended{
				chunk: c, tick: ticks[c], firstSeq: seqs[c] + 1, count: count,
				run: runBytes, wh: uint64(seed)<<32 | uint64(i),
			}
			if err := l.AppendTick(a.chunk, a.tick, a.firstSeq, a.count, a.run, a.wh); err != nil {
				t.Fatalf("op %d append: %v", i, err)
			}
			seqs[c] += int64(count)
			model[c] = append(model[c], a)
		case r < 0.85:
			ticks[c] += 1 + uint64(rng.Intn(5))
			l.Watermark(c, ticks[c])
		case r < 0.97:
			if err := l.Barrier(context.Background()); err != nil {
				t.Fatalf("op %d barrier: %v", i, err)
			}
			for cc := 0; cc < chunks; cc++ {
				if n := len(model[cc]); n > 0 {
					last := model[cc][n-1]
					acked[cc] = Tip{Tick: last.tick, Seq: last.firstSeq + int64(last.count) - 1}
				}
			}
		default:
			epoch++
			if err := l.AdvanceEpoch(context.Background(), c, epoch); err != nil {
				t.Fatalf("op %d epoch: %v", i, err)
			}
		}
	}
	// Crash: no Close, no final barrier. Give the syncer a moment to
	// drain in-flight work so the "full" image is meaningful, then image.
	time.Sleep(20 * time.Millisecond)
	for img, includeUnsynced := range map[string]bool{"synced": false, "full": true} {
		dir := t.TempDir()
		if err := fs.image(dir, includeUnsynced); err != nil {
			t.Fatal(err)
		}
		got := make(map[string][]codec.Record)
		tips, err := Scan(dir, keysFor(chunks), func(chunk string, r codec.Record) error {
			r.Records = append([]byte(nil), r.Records...)
			got[chunk] = append(got[chunk], r)
			return nil
		})
		if err != nil {
			t.Fatalf("%s image: scan: %v", img, err)
		}
		for c := 0; c < chunks; c++ {
			name := fmt.Sprintf("chunk-%d", c)
			recs := got[name]
			if len(recs) > len(model[c]) {
				t.Fatalf("%s image: %s delivered %d records, appended %d", img, name, len(recs), len(model[c]))
			}
			for i, r := range recs {
				a := model[c][i]
				if r.Tick != a.tick || r.FirstSeq != a.firstSeq || r.Count != a.count ||
					r.WH != a.wh || !bytes.Equal(r.Records, a.run) {
					t.Fatalf("%s image: %s[%d] is not the appended record", img, name, i)
				}
			}
			if tip := tips[name]; tip.Tick < acked[c].Tick || tip.Seq < acked[c].Seq {
				t.Fatalf("%s image: %s recovered to %+v, barrier acked %+v", img, name, tip, acked[c])
			}
		}
		if img != "synced" {
			continue
		}
		// Restart on the realistic image: the successor resumes from the
		// scan tips and the combined history stays dense.
		specs := specsFor(chunks)
		for c := range specs {
			tip := tips[specs[c].Name]
			specs[c].StartTick, specs[c].StartSeq = tip.Tick+1, tip.Seq
		}
		l2, err := Create(Config{Dir: dir, Generation: 2, Chunks: specs})
		if err != nil {
			t.Fatalf("restart: %v", err)
		}
		for c := 0; c < chunks; c++ {
			name := fmt.Sprintf("chunk-%d", c)
			tip := tips[name]
			for k := 0; k < 3; k++ {
				tick := tip.Tick + uint64(k) + 1
				seq := tip.Seq + int64(k) + 1
				if err := l2.AppendTick(c, tick, seq, 1, run(int(seq)), 0); err != nil {
					t.Fatalf("restart append: %v", err)
				}
			}
		}
		if err := l2.Close(); err != nil {
			t.Fatal(err)
		}
		count := 0
		_, err = Scan(dir, keysFor(chunks), func(string, codec.Record) error { count++; return nil })
		if err != nil {
			t.Fatalf("post-restart scan: %v", err)
		}
		want := 0
		for c := 0; c < chunks; c++ {
			want = want + len(got[fmt.Sprintf("chunk-%d", c)]) + 3
		}
		if count != want {
			t.Fatalf("post-restart scan delivered %d records, want %d", count, want)
		}
	}
	fs.setStall(false)
	l.Close()
}
