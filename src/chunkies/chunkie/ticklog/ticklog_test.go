package ticklog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// run builds a one-event EventRecord run whose payload encodes n, so a
// replayed record's bytes identify exactly which append produced it.
func run(n int) []byte {
	return codec.AppendEventRecord(nil, uint64(0x1000+n), 7, uint64(0xA0+n), []byte{byte(n), byte(n >> 8)})
}

type appended struct {
	chunk    int
	tick     uint64
	firstSeq int64
	count    uint16
	run      []byte
	wh       uint64
}

// script appends a deterministic interleaved history and returns it. A
// barrier every few ticks bounds each commit group, so rotation and
// durability points land throughout the run instead of only at close.
func script(t *testing.T, l *Log, chunks, ticks int) []appended {
	t.Helper()
	var all []appended
	seqs := make([]int64, chunks)
	for tick := 1; tick <= ticks; tick++ {
		for c := 0; c < chunks; c++ {
			if (tick+c)%3 == 0 { // idle tick for this chunk
				l.Watermark(c, uint64(tick))
				continue
			}
			a := appended{
				chunk: c, tick: uint64(tick), firstSeq: seqs[c] + 1, count: 1,
				run: run(tick*8 + c), wh: uint64(tick)<<8 | uint64(c),
			}
			if err := l.AppendTick(a.chunk, a.tick, a.firstSeq, a.count, a.run, a.wh); err != nil {
				t.Fatalf("append chunk %d tick %d: %v", c, tick, err)
			}
			seqs[c] += int64(a.count)
			all = append(all, a)
		}
		if tick%5 == 0 {
			if err := l.Barrier(context.Background()); err != nil {
				t.Fatalf("barrier at tick %d: %v", tick, err)
			}
		}
	}
	return all
}

func keysFor(chunks int) []ChunkKey {
	keys := make([]ChunkKey, chunks)
	for i := range keys {
		keys[i] = ChunkKey{Name: fmt.Sprintf("chunk-%d", i), Lineage: uint32(10 + i)}
	}
	return keys
}

func specsFor(chunks int) []ChunkSpec {
	specs := make([]ChunkSpec, chunks)
	for i := range specs {
		specs[i] = ChunkSpec{Name: fmt.Sprintf("chunk-%d", i), Lineage: uint32(10 + i), Epoch: 1, StartTick: 1, StartSeq: 0}
	}
	return specs
}

// collect scans and returns delivered records per chunk name.
func collect(t *testing.T, dir string, keys []ChunkKey) (map[string][]codec.Record, Tips) {
	t.Helper()
	got := map[string][]codec.Record{}
	tips, err := Scan(dir, keys, func(chunk string, r codec.Record) error {
		r.Records = append([]byte(nil), r.Records...)
		got[chunk] = append(got[chunk], r)
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return got, tips
}

func checkDelivered(t *testing.T, all []appended, got map[string][]codec.Record) {
	t.Helper()
	byChunk := map[string][]appended{}
	for _, a := range all {
		name := fmt.Sprintf("chunk-%d", a.chunk)
		byChunk[name] = append(byChunk[name], a)
	}
	for name, want := range byChunk {
		recs := got[name]
		if len(recs) != len(want) {
			t.Fatalf("%s: %d records delivered, want %d", name, len(recs), len(want))
		}
		for i, a := range want {
			r := recs[i]
			if r.Tick != a.tick || r.FirstSeq != a.firstSeq || r.Count != a.count ||
				r.WH != a.wh || !bytes.Equal(r.Records, a.run) {
				t.Fatalf("%s[%d]: got %+v want %+v", name, i, r, a)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(3), SegmentBytes: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	all := script(t, l, 3, 40)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, tips := collect(t, dir, keysFor(3))
	checkDelivered(t, all, got)
	for c := 0; c < 3; c++ {
		name := fmt.Sprintf("chunk-%d", c)
		last := got[name][len(got[name])-1]
		if tips[name].Seq != last.FirstSeq+int64(last.Count)-1 {
			t.Fatalf("%s tip seq %d, want %d", name, tips[name].Seq, last.FirstSeq)
		}
		if tips[name].Tick < last.Tick {
			t.Fatalf("%s tip tick %d before last record %d", name, tips[name].Tick, last.Tick)
		}
	}
}

func TestBarrierMakesDurable(t *testing.T) {
	dir := t.TempDir()
	// A long sync interval: only the barrier can make anything durable.
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1), SyncInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.AppendTick(0, 5, 1, 1, run(1), 42); err != nil {
		t.Fatal(err)
	}
	if d := l.Durable(0); d.Seq != 0 {
		t.Fatalf("durable before barrier: %+v", d)
	}
	if err := l.Barrier(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := l.Durable(0); d.Tick != 5 || d.Seq != 1 {
		t.Fatalf("durable after barrier: %+v", d)
	}
	if lag := l.DurableLag(); lag != 0 {
		t.Fatalf("durable lag %v after barrier", lag)
	}
}

func TestRotationBySize(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(2), SegmentBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	all := script(t, l, 2, 60)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("expected several segments at 512B, got %d", len(segs))
	}
	// Headers chain: each segment's first ticks continue from history.
	for i := 1; i < len(segs); i++ {
		for _, ch := range segs[i].header.Chunks {
			prev, ok := firstTickOf(segs[i-1].header, ch.Name)
			if !ok || ch.FirstTick < prev {
				t.Fatalf("segment %d first tick %d regressed from %d", i, ch.FirstTick, prev)
			}
		}
	}
	got, _ := collect(t, dir, keysFor(2))
	checkDelivered(t, all, got)
}

func TestEpochBarrierRotation(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(2)})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	if err := l.AdvanceEpoch(context.Background(), 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTick(0, 2, 2, 1, run(2), 2); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("epoch advance must rotate: %d segments", len(segs))
	}
	if e := segs[0].header.Chunks[0].Epoch; e != 1 {
		t.Fatalf("old segment epoch %d", e)
	}
	if e := segs[1].header.Chunks[0].Epoch; e != 2 {
		t.Fatalf("new segment epoch %d", e)
	}
	if e := segs[1].header.Chunks[1].Epoch; e != 1 {
		t.Fatalf("untouched chunk's epoch moved to %d", e)
	}
	// No segment holds records under two epochs: the promoted chunk's
	// records split exactly at the barrier.
	got, _ := collect(t, dir, keysFor(2))
	if n := len(got["chunk-0"]); n != 2 {
		t.Fatalf("delivered %d records", n)
	}
}

func TestAppendValidation(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.AppendTick(1, 1, 1, 1, run(1), 0); err == nil {
		t.Fatal("out-of-range chunk accepted")
	}
	if err := l.AppendTick(0, 1, 1, 0, nil, 0); err == nil {
		t.Fatal("empty tick accepted")
	}
	if err := l.AppendTick(0, 5, 1, 2, run(1), 0); err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTick(0, 5, 3, 1, run(2), 0); err == nil {
		t.Fatal("replayed tick accepted")
	}
	if err := l.AppendTick(0, 6, 4, 1, run(3), 0); err == nil {
		t.Fatal("seq gap accepted")
	}
	// Validation failures are the caller's bug, not a log fault: the
	// next correct append still lands.
	if err := l.AppendTick(0, 6, 3, 1, run(4), 0); err != nil {
		t.Fatalf("valid append after rejections: %v", err)
	}
}

func TestWatermarkAdvancesReplayFrontier(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{
		Dir: dir, Generation: 1, Chunks: specsFor(1),
		SyncInterval: time.Millisecond, WatermarkEvery: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	l.Watermark(0, 900)
	deadline := time.Now().Add(5 * time.Second)
	for l.Durable(0).Tick < 900 {
		if time.Now().After(deadline) {
			t.Fatalf("watermark never became durable: %+v", l.Durable(0))
		}
		time.Sleep(time.Millisecond)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	_, tips := collect(t, dir, keysFor(1))
	if tips["chunk-0"].Tick != 900 || tips["chunk-0"].Seq != 1 {
		t.Fatalf("tips %+v", tips["chunk-0"])
	}
}

// TestResumeAcrossGenerations is the pod-restart story: scan, restart
// from the tips under a new generation, and verify one dense history.
func TestResumeAcrossGenerations(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(2)})
	if err != nil {
		t.Fatal(err)
	}
	all := script(t, l, 2, 20)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	_, tips := collect(t, dir, keysFor(2))

	specs := specsFor(2)
	for i := range specs {
		tip := tips[specs[i].Name]
		specs[i].StartTick, specs[i].StartSeq = tip.Tick+1, tip.Seq
	}
	l2, err := Create(Config{Dir: dir, Generation: 2, Chunks: specs})
	if err != nil {
		t.Fatal(err)
	}
	seqs := map[int]int64{0: tips["chunk-0"].Seq, 1: tips["chunk-1"].Seq}
	for tick := tips["chunk-0"].Tick + 1; tick <= tips["chunk-0"].Tick+10; tick++ {
		for c := 0; c < 2; c++ {
			a := appended{chunk: c, tick: tick, firstSeq: seqs[c] + 1, count: 1, run: run(int(tick)*8 + c), wh: tick}
			if err := l2.AppendTick(a.chunk, a.tick, a.firstSeq, a.count, a.run, a.wh); err != nil {
				t.Fatalf("gen2 append: %v", err)
			}
			seqs[c]++
			all = append(all, a)
		}
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, dir, keysFor(2))
	checkDelivered(t, all, got)
}

// TestFencedWriterCapped proves a dead generation's unacked tail cannot
// override its successor: records at or past the next generation's first
// tick are skipped.
func TestFencedWriterCapped(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1)})
	if err != nil {
		t.Fatal(err)
	}
	for tick := uint64(1); tick <= 10; tick++ {
		if err := l.AppendTick(0, tick, int64(tick), 1, run(int(tick)), tick); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// The successor activated believing only ticks 1..8 were durable —
	// as if the fenced writer's final commit group (9, 10) landed after
	// the successor had already scanned.
	specs := specsFor(1)
	specs[0].StartTick, specs[0].StartSeq = 9, 8
	l2, err := Create(Config{Dir: dir, Generation: 2, Chunks: specs})
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.AppendTick(0, 9, 9, 1, run(1009), 9009); err != nil {
		t.Fatal(err)
	}
	if err := l2.Close(); err != nil {
		t.Fatal(err)
	}
	got, tips := collect(t, dir, keysFor(1))
	recs := got["chunk-0"]
	if len(recs) != 9 {
		t.Fatalf("delivered %d records, want 9 (1..8 + successor's 9)", len(recs))
	}
	if recs[8].Tick != 9 || recs[8].WH != 9009 {
		t.Fatalf("tick 9 came from the fenced writer: %+v", recs[8])
	}
	if tips["chunk-0"].Seq != 9 {
		t.Fatalf("tips %+v", tips["chunk-0"])
	}
}

func TestScanRefusesGap(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1), SegmentBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	script(t, l, 1, 40)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("need ≥3 segments, got %d", len(segs))
	}
	if err := os.Remove(segs[1].path); err != nil {
		t.Fatal(err)
	}
	_, err = Scan(dir, keysFor(1), func(string, codec.Record) error { return nil })
	if !errors.Is(err, ErrGap) {
		t.Fatalf("scan over a missing segment: %v, want ErrGap", err)
	}
}

func TestScanReportsCorruptHeader(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1), SegmentBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	all := script(t, l, 1, 40)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 3 {
		t.Fatalf("need ≥3 segments, got %d", len(segs))
	}
	raw, err := os.ReadFile(segs[1].path)
	if err != nil {
		t.Fatal(err)
	}
	raw[8] ^= 0xFF
	if err := os.WriteFile(segs[1].path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var delivered int
	tips, err := Scan(dir, keysFor(1), func(string, codec.Record) error { delivered++; return nil })
	if !errors.Is(err, codec.ErrCorrupt) {
		t.Fatalf("scan over damaged header: %v, want ErrCorrupt", err)
	}
	// The intact prefix was still replayed, and tips are honest about
	// where replay stopped.
	var wantPrefix int
	bound, _ := firstTickOf(segs[1].header, "chunk-0")
	for _, a := range all {
		if a.tick < bound {
			wantPrefix++
		}
	}
	if delivered != wantPrefix || tips["chunk-0"].Tick >= bound {
		t.Fatalf("delivered %d (want %d), tips %+v, bound %d", delivered, wantPrefix, tips["chunk-0"], bound)
	}
}

func TestTrim(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(2), SegmentBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	all := script(t, l, 2, 60)
	if err := l.Barrier(context.Background()); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 4 {
		t.Fatalf("need ≥4 segments, got %d", len(segs))
	}

	// Nothing covered: nothing trimmed.
	if n, err := l.Trim(Tips{}, 0); err != nil || n != 0 {
		t.Fatalf("uncovered trim removed %d, err %v", n, err)
	}
	// A retention floor keeps even covered segments.
	full := Tips{"chunk-0": {Tick: 999, Seq: 999}, "chunk-1": {Tick: 999, Seq: 999}}
	if n, err := l.Trim(full, time.Hour); err != nil || n != 0 {
		t.Fatalf("floored trim removed %d, err %v", n, err)
	}
	// Fully covered with no floor: everything but the tail goes.
	n, err := l.Trim(full, 0)
	if err != nil || n != len(segs)-1 {
		t.Fatalf("trim removed %d, want %d, err %v", n, len(segs)-1, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	// Replay from the "checkpoint" frontier still works over what's left.
	var maxTick uint64
	for _, a := range all {
		if a.tick > maxTick {
			maxTick = a.tick
		}
	}
	keys := keysFor(2)
	left, err := readSegmentHeaders(dir)
	if err != nil || len(left) != 1 {
		t.Fatalf("%d segments left, err %v", len(left), err)
	}
	for i := range keys {
		first, ok := firstTickOf(left[0].header, keys[i].Name)
		if !ok {
			t.Fatalf("chunk missing from surviving header")
		}
		keys[i].AfterTick = first - 1
		for _, a := range all {
			if fmt.Sprintf("chunk-%d", a.chunk) == keys[i].Name && a.tick <= first-1 {
				keys[i].AfterSeq = a.firstSeq + int64(a.count) - 1
			}
		}
	}
	if _, err := Scan(dir, keys, func(string, codec.Record) error { return nil }); err != nil {
		t.Fatalf("scan after trim: %v", err)
	}
}

func TestCreateRejectsBadConfig(t *testing.T) {
	if _, err := Create(Config{Dir: t.TempDir(), Generation: 1}); err == nil {
		t.Fatal("no chunks accepted")
	}
}

func TestCloseIsNotAFault(t *testing.T) {
	dir := t.TempDir()
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-l.Faults():
		t.Fatalf("clean close raised a fault: %v", f)
	default:
	}
}

func TestTempSegmentsAreInvisible(t *testing.T) {
	dir := t.TempDir()
	// A crashed activation's leftover temp file must be ignored by the
	// reader and cleaned by the next writer.
	if err := os.WriteFile(filepath.Join(dir, "00000002-00000000.chkw.tmp"), []byte("torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Create(Config{Dir: dir, Generation: 1, Chunks: specsFor(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AppendTick(0, 1, 1, 1, run(1), 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	got, _ := collect(t, dir, keysFor(1))
	if len(got["chunk-0"]) != 1 {
		t.Fatalf("delivered %d", len(got["chunk-0"]))
	}
}
