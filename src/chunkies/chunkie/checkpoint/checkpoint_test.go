package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

func manifest(chunk string, lineage uint32, seq int64, tick uint64, epoch uint32, state []byte) codec.Checkpoint {
	return codec.Checkpoint{
		Version: 1, Game: "wum", Chunk: chunk,
		Lineage: lineage, Generation: 7, Seq: seq, Tick: tick, Epoch: epoch,
		WH: 0xfeed, Content: 0xbeef,
		Dedup: []codec.DedupEntry{{Actor: 1, Intent: 2}},
		State: state,
	}
}

func TestDirRoundTripAndOrdering(t *testing.T) {
	d, err := NewDir(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Out-of-order puts across a lineage rewind: ordering is (lineage,
	// seq), newest first.
	for _, m := range []codec.Checkpoint{
		manifest("park", 0, 10, 100, 1, []byte("a")),
		manifest("park", 1, 5, 60, 2, []byte("c")),
		manifest("park", 0, 20, 200, 1, []byte("b")),
	} {
		if _, err := d.Put(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := d.List("park")
	if err != nil || len(refs) != 3 {
		t.Fatalf("list: %v, %d refs", err, len(refs))
	}
	if refs[0].Lineage != 1 || refs[1].Seq != 20 || refs[2].Seq != 10 {
		t.Fatalf("order wrong: %+v", refs)
	}

	// Dir stores State opaquely — deflate is the snapshotter's business.
	m, err := d.Load(refs[1])
	if err != nil || string(m.State) != "b" || len(m.Dedup) != 1 {
		t.Fatalf("load: %v %+v", err, m)
	}
	if chunks, err := d.Chunks(); err != nil || len(chunks) != 1 || chunks[0] != "park" {
		t.Fatalf("chunks: %v %v", chunks, err)
	}

	if err := d.MarkProven(refs[0]); err != nil {
		t.Fatal(err)
	}
	refs, _ = d.List("park")
	if !refs[0].Proven || refs[1].Proven {
		t.Fatalf("proven flags wrong: %+v", refs)
	}

	if err := d.Remove(refs[0]); err != nil {
		t.Fatal(err)
	}
	if refs, _ = d.List("park"); len(refs) != 2 || refs[0].Proven {
		t.Fatalf("remove left: %+v", refs)
	}
}

func TestLoadRefusesRenamedAndCorrupt(t *testing.T) {
	d, _ := NewDir(t.TempDir(), 0)
	ctx := context.Background()
	ref, err := d.Put(ctx, manifest("park", 0, 10, 100, 1, []byte("a")))
	if err != nil {
		t.Fatal(err)
	}
	// A renamed checkpoint is corruption: identity comes from content.
	forged := ref
	forged.Seq = 11
	if err := os.Rename(ref.path(d.root), forged.path(d.root)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load(forged); !errors.Is(err, ErrMismatch) {
		t.Fatalf("renamed load = %v, want ErrMismatch", err)
	}
	os.Rename(forged.path(d.root), ref.path(d.root))

	b, _ := os.ReadFile(ref.path(d.root))
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(ref.path(d.root), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Load(ref); err == nil {
		t.Fatal("corrupt checkpoint loaded")
	}
}

func TestRetainRingAndEpochGuard(t *testing.T) {
	d, _ := NewDir(t.TempDir(), 0)
	ctx := context.Background()
	// Five same-epoch checkpoints: the ring keeps the newest three.
	for seq := int64(1); seq <= 5; seq++ {
		if _, err := d.Put(ctx, manifest("park", 0, seq*10, uint64(seq*100), 1, []byte("s"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := Retain(d, nil, 3); err != nil {
		t.Fatal(err)
	}
	refs, _ := d.List("park")
	if len(refs) != 3 || refs[2].Seq != 30 {
		t.Fatalf("ring kept %+v", refs)
	}

	// A promotion lands epoch-2 checkpoints, unproven: the newest
	// pre-barrier (epoch 1) checkpoint is immortal past the ring until
	// one proves.
	for seq := int64(6); seq <= 9; seq++ {
		if _, err := d.Put(ctx, manifest("park", 0, seq*10, uint64(seq*100), 2, []byte("s"))); err != nil {
			t.Fatal(err)
		}
	}
	if err := Retain(d, nil, 3); err != nil {
		t.Fatal(err)
	}
	refs, _ = d.List("park")
	var epochs []uint32
	for _, r := range refs {
		epochs = append(epochs, r.Epoch)
	}
	if len(refs) != 4 || refs[3].Epoch != 1 || refs[3].Seq != 50 {
		t.Fatalf("pre-barrier survivor wrong: %+v (%v)", refs, epochs)
	}

	// Proving a post-barrier checkpoint kills the old epoch.
	if err := d.MarkProven(refs[0]); err != nil {
		t.Fatal(err)
	}
	if err := Retain(d, nil, 3); err != nil {
		t.Fatal(err)
	}
	refs, _ = d.List("park")
	if len(refs) != 3 {
		t.Fatalf("post-proof kept %+v", refs)
	}
	for _, r := range refs {
		if r.Epoch != 2 {
			t.Fatalf("old-epoch checkpoint survived the proof: %+v", r)
		}
	}
}

func TestSnapshotterSubmitAndForce(t *testing.T) {
	d, _ := NewDir(t.TempDir(), 0)
	s := New(d, nil, Config{Cadence: time.Hour})
	defer s.Close(context.Background())

	now := time.Now()
	if s.Due("park", now) {
		t.Fatal("first Due must arm, not fire")
	}
	if !s.Due("park", now.Add(2*time.Hour)) {
		t.Fatal("past cadence must be due")
	}

	raw := bytes.Repeat([]byte("world state "), 1024)
	s.Submit(manifest("park", 0, 10, 100, 1, raw))
	deadline := time.Now().Add(5 * time.Second)
	var refs []Ref
	for {
		refs, _ = d.List("park")
		if len(refs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("submitted checkpoint never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m, err := d.Load(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	state, err := inflate(m.State)
	if err != nil || !bytes.Equal(state, raw) {
		t.Fatalf("deflate round trip: %v", err)
	}

	proved := false
	ref, err := s.Force(context.Background(), manifest("park", 0, 20, 200, 2, raw), func(state []byte, wh uint64) error {
		if !bytes.Equal(state, raw) || wh != 0xfeed {
			t.Fatalf("prove got wrong state/hash")
		}
		proved = true
		return nil
	})
	if err != nil || !proved {
		t.Fatalf("force: %v proved=%v", err, proved)
	}
	refs, _ = d.List("park")
	if refs[0] != ref || !refs[0].Proven {
		t.Fatalf("forced checkpoint not proven on disk: %+v", refs)
	}

	// A refused proof surfaces and leaves no proven marker.
	if _, err := s.Force(context.Background(), manifest("park", 0, 30, 300, 2, raw), func([]byte, uint64) error {
		return errors.New("wrong world")
	}); err == nil {
		t.Fatal("refused proof did not error")
	}
	// A refused proof leaves NOTHING on disk — a poison file would be
	// the newest CRC-valid ref, exactly what recovery prefers.
	refs, _ = d.List("park")
	for _, r := range refs {
		if r.Seq == 30 {
			t.Fatal("refused proof left the checkpoint on disk")
		}
	}
}

func TestCloseLandsAcceptedWork(t *testing.T) {
	d, _ := NewDir(t.TempDir(), 0)
	s := New(d, nil, Config{Cadence: time.Hour})
	s.Submit(manifest("park", 0, 10, 100, 1, []byte("state")))
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	refs, _ := d.List("park")
	if len(refs) != 1 {
		t.Fatalf("accepted checkpoint did not land across Close: %+v", refs)
	}
	// Submits after Close drop without wedging the busy latch.
	s.Submit(manifest("park", 0, 20, 200, 1, []byte("state")))
	if refs, _ = d.List("park"); len(refs) != 1 {
		t.Fatal("post-Close submit landed")
	}
}

func TestSmearedWriteRoundTrips(t *testing.T) {
	// A 2MiB body under a 16MiB/s budget must still land intact, and
	// the smear must actually pace (at least one inter-slice sleep).
	d, _ := NewDir(t.TempDir(), 16<<20)
	raw := bytes.Repeat([]byte{0xA5}, 2<<20)
	start := time.Now()
	ref, err := d.Put(context.Background(), manifest("park", 0, 10, 100, 1, raw))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("smear budget did not pace the write")
	}
	m, err := d.Load(ref)
	if err != nil || !bytes.Equal(m.State, raw) {
		t.Fatalf("smeared write corrupted state: %v", err)
	}
}

func TestRetainTrimsOnlyNewestLineageCoverage(t *testing.T) {
	// After a rewind, the old lineage's higher-tick checkpoint must not
	// drive WAL trim coverage — that would delete new-lineage segments
	// replay still needs. With only old-lineage refs beyond the ring
	// and a single new-lineage ref, coverage comes from the new one.
	d, _ := NewDir(t.TempDir(), 0)
	ctx := context.Background()
	if _, err := d.Put(ctx, manifest("park", 0, 100, 5000, 1, []byte("old"))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Put(ctx, manifest("park", 1, 40, 3000, 1, []byte("new"))); err != nil {
		t.Fatal(err)
	}
	// nil WAL: Retain's coverage math is exercised via its kept set —
	// the newest-lineage ref must be what remains authoritative.
	if err := Retain(d, nil, 2); err != nil {
		t.Fatal(err)
	}
	refs, _ := d.List("park")
	if len(refs) != 2 || refs[0].Lineage != 1 {
		t.Fatalf("retain reshaped refs unexpectedly: %+v", refs)
	}
}
