package ticklog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// segmentBounds walks a segment's record region and returns each record's
// end offset plus the decoded record, straight off the intact file.
func segmentBounds(t *testing.T, path string, headerLen int) (ends []int, recs []codec.Record, dataEnd int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	off := headerLen
	for {
		rec, n, err := codec.ReadRecord(raw[off:])
		if err != nil {
			if err != codec.ErrEnd && err != codec.ErrTornTail {
				t.Fatalf("intact segment misreads at %d: %v", off, err)
			}
			return ends, recs, off
		}
		rec.Records = append([]byte(nil), rec.Records...)
		off += n
		ends = append(ends, off)
		recs = append(recs, rec)
	}
}

// TestEveryTruncationRecovers is the exhaustive crash-point sweep: for
// every byte-length prefix of the final segment, recovery must yield
// exactly the records whose frames survived whole — a clean prefix, never
// an error, never an invented byte. The zero-fill stands in for the
// preallocated tail a real crash leaves.
func TestEveryTruncationRecovers(t *testing.T) {
	src := t.TempDir()
	l, err := Create(Config{Dir: src, Generation: 1, Chunks: specsFor(2), SegmentBytes: 1 << 11})
	if err != nil {
		t.Fatal(err)
	}
	script(t, l, 2, 30)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(src)
	if err != nil {
		t.Fatal(err)
	}
	last := segs[len(segs)-1]
	ends, recs, dataEnd := segmentBounds(t, last.path, last.headerLen)
	if len(ends) < 4 {
		t.Fatalf("final segment holds only %d records", len(ends))
	}
	// Records delivered from the intact earlier segments, for the
	// expectation baseline.
	priorByChunk := map[string][]codec.Record{}
	for _, s := range segs[:len(segs)-1] {
		_, srecs, _ := segmentBounds(t, s.path, s.headerLen)
		for _, r := range srecs {
			if r.Type == codec.RecordTick {
				name := s.header.Chunks[r.Chunk].Name
				priorByChunk[name] = append(priorByChunk[name], r)
			}
		}
	}
	raw, err := os.ReadFile(last.path)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	for _, s := range segs[:len(segs)-1] {
		b, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, filepath.Base(s.path)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(work, filepath.Base(last.path))

	step := 1
	if testing.Short() {
		step = 13
	}
	for cut := last.headerLen; cut <= dataEnd+8; cut += step {
		img := make([]byte, len(raw))
		copy(img, raw[:cut])
		if err := os.WriteFile(target, img, 0o600); err != nil {
			t.Fatal(err)
		}
		want := map[string][]codec.Record{}
		for name, rs := range priorByChunk {
			want[name] = append([]codec.Record(nil), rs...)
		}
		for i, end := range ends {
			if end > cut {
				break
			}
			if recs[i].Type == codec.RecordTick {
				name := last.header.Chunks[recs[i].Chunk].Name
				want[name] = append(want[name], recs[i])
			}
		}
		got := map[string][]codec.Record{}
		if _, err := Scan(work, keysFor(2), func(chunk string, r codec.Record) error {
			r.Records = append([]byte(nil), r.Records...)
			got[chunk] = append(got[chunk], r)
			return nil
		}); err != nil {
			t.Fatalf("cut %d: scan errored: %v", cut, err)
		}
		for name, w := range want {
			g := got[name]
			if len(g) != len(w) {
				t.Fatalf("cut %d: %s delivered %d records, want %d", cut, name, len(g), len(w))
			}
			for i := range w {
				if g[i].Tick != w[i].Tick || g[i].FirstSeq != w[i].FirstSeq ||
					g[i].WH != w[i].WH || !bytes.Equal(g[i].Records, w[i].Records) {
					t.Fatalf("cut %d: %s[%d] disagrees with what was appended", cut, name, i)
				}
			}
		}
	}
}

// TestEveryFlipIsCaught is the corruption sweep: any flipped byte in the
// record region must yield exactly the records before the damaged frame
// and either silence (the flip forged a tail) or ErrCorrupt — never a
// record past the damage, never altered bytes, never a panic.
func TestEveryFlipIsCaught(t *testing.T) {
	src := t.TempDir()
	l, err := Create(Config{Dir: src, Generation: 1, Chunks: specsFor(1)})
	if err != nil {
		t.Fatal(err)
	}
	script(t, l, 1, 24)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegmentHeaders(src)
	if err != nil || len(segs) != 1 {
		t.Fatalf("want one segment, got %d err %v", len(segs), err)
	}
	seg := segs[0]
	ends, recs, dataEnd := segmentBounds(t, seg.path, seg.headerLen)
	raw, err := os.ReadFile(seg.path)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	target := filepath.Join(work, filepath.Base(seg.path))
	step := 1
	if testing.Short() {
		step = 7
	}
	for pos := seg.headerLen; pos < dataEnd; pos += step {
		img := append([]byte(nil), raw...)
		img[pos] ^= 0xFF
		if err := os.WriteFile(target, img, 0o600); err != nil {
			t.Fatal(err)
		}
		// Which record holds the damage?
		damaged := len(ends)
		for i, end := range ends {
			if pos < end {
				damaged = i
				break
			}
		}
		var want []codec.Record
		for i := 0; i < damaged; i++ {
			if recs[i].Type == codec.RecordTick {
				want = append(want, recs[i])
			}
		}
		var got []codec.Record
		_, err := Scan(work, keysFor(1), func(chunk string, r codec.Record) error {
			r.Records = append([]byte(nil), r.Records...)
			got = append(got, r)
			return nil
		})
		if err != nil && !errors.Is(err, codec.ErrCorrupt) {
			t.Fatalf("flip at %d: unexpected error class: %v", pos, err)
		}
		if len(got) != len(want) {
			t.Fatalf("flip at %d (record %d): delivered %d records, want %d (err %v)",
				pos, damaged, len(got), len(want), err)
		}
		for i := range want {
			if got[i].Tick != want[i].Tick || !bytes.Equal(got[i].Records, want[i].Records) {
				t.Fatalf("flip at %d: delivered altered record %d", pos, i)
			}
		}
	}
}

// TestKillNineRecovers crashes a real appender process at random points
// — the only tier that exercises the page cache honestly — and verifies
// recovery yields a dense prefix containing everything the child saw
// acknowledged as durable.
func TestKillNineRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess crash drill")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for round := 0; round < 5; round++ {
		dir := t.TempDir()
		cmd := exec.Command(exe, "-test.run", "TestKillNineRecovers")
		cmd.Env = append(os.Environ(), "TICKLOG_CRASH_CHILD=1", "TICKLOG_CRASH_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Duration(20+rng.Intn(150)) * time.Millisecond)
		cmd.Process.Kill()
		cmd.Wait()

		acked := readAckFile(t, dir)
		var got []codec.Record
		tips, err := Scan(dir, []ChunkKey{{Name: "chunk-0", Lineage: 10}}, func(_ string, r codec.Record) error {
			r.Records = append([]byte(nil), r.Records...)
			got = append(got, r)
			return nil
		})
		if err != nil {
			t.Fatalf("round %d: scan after kill: %v", round, err)
		}
		for i, r := range got {
			if r.FirstSeq != int64(i+1) || !bytes.Equal(r.Records, run(int(r.FirstSeq))) {
				t.Fatalf("round %d: record %d is not the dense prefix: %+v", round, i, r)
			}
		}
		if tip := tips["chunk-0"]; tip.Seq < acked.Seq || tip.Tick < acked.Tick {
			t.Fatalf("round %d: recovered %+v but the child saw %+v acked", round, tip, acked)
		}
	}
}

func readAckFile(t *testing.T, dir string) Tip {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "acked"))
	if os.IsNotExist(err) {
		return Tip{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var tip Tip
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var next Tip
		if _, err := fmt.Sscanf(line, "%d %d", &next.Tick, &next.Seq); err == nil {
			tip = next
		}
	}
	return tip
}

// crashChild runs inside the doomed subprocess: append at full speed with
// small segments (so kills land mid-rotation too), barrier periodically,
// and record each acknowledged frontier with its own fsync so the parent
// knows what recovery owes it.
func crashChild() {
	dir := os.Getenv("TICKLOG_CRASH_DIR")
	l, err := Create(Config{
		Dir: dir, Generation: 1,
		Chunks:       []ChunkSpec{{Name: "chunk-0", Lineage: 10, Epoch: 1, StartTick: 1}},
		SegmentBytes: 1 << 10, SyncInterval: 2 * time.Millisecond,
	})
	if err != nil {
		panic(err)
	}
	ack, err := os.OpenFile(filepath.Join(dir, "acked"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0o600)
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	for i := 1; ; i++ {
		if err := l.AppendTick(0, uint64(i), int64(i), 1, run(i), uint64(i)); err != nil {
			panic(err)
		}
		if i%20 == 0 {
			if err := l.Barrier(ctx); err != nil {
				panic(err)
			}
			d := l.Durable(0)
			fmt.Fprintf(ack, "%d %d\n", d.Tick, d.Seq)
		}
	}
}
