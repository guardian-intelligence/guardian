package chunkie

// The recovery ladder against a real volume: a live shadowed world
// plays traffic, checkpoints mid-history, plays more, and dies; the
// ladder must put the same world back — or refuse, on exactly the
// rungs the design refuses.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/checkpoint"
	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/postflight/controlplane/pgtest"
)

type builtWorld struct {
	dir    string
	st     checkpoint.Store
	module []byte
	cw, pw [32]byte
	pgTip  ticklog.Tip // the journal's final frontier: what recovery chases
}

// buildShadowedWorld runs the full write path: journal + shadow WAL +
// checkpoint lane, traffic before and after one checkpoint, then a
// clean close (which barriers the WAL and releases the writer lock).
func buildShadowedWorld(t *testing.T) builtWorld {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	j := journal.NewPg(pool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	module := toyModule(t)
	dir := t.TempDir()
	st, err := checkpoint.NewDir(filepath.Join(dir, "checkpoints"), 0)
	if err != nil {
		t.Fatal(err)
	}

	clock := fixedClock(wallEpoch.Add(time.Hour))
	a, err := openAuthority(ctx, "chunk-ladder", module, nil, toyVocab(), j, toyMods(module), clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	openSeq, openTick := a.lastSeq, a.host.Tick()
	a.wal = newShadowFactory(dir, st, "toy")("chunk-ladder", a.host.Epoch(), openTick, openSeq, shadowBoot{})
	if a.wal == nil {
		t.Fatal("shadow WAL failed to open")
	}
	cw, pw := a.mods.client.Sum(), a.simSum

	s := &session{sub: "alice", actorID: codec.ActorFor("alice"), out: make(chan []byte, 256)}
	a.stageIntent(s, 1, kJoin, nil)
	a.tickOnce()
	for i := 0; i < 20; i++ {
		a.stageIntent(s, uint64(10+i), kMove, move(int32(i%7-3)))
		a.tickOnce()
	}

	// The mid-history checkpoint, exactly as tickOnce mints one.
	a.wal.submitCheckpoint(codec.Checkpoint{
		Version: 1, Game: "toy", Chunk: a.name,
		Lineage: 0, Seq: a.lastSeq, Tick: a.host.Tick() - 1, Epoch: a.host.Epoch(),
		WH: a.host.Hash(), Content: a.terrain,
		CW: cw, PW: pw,
		Dedup: append([]codec.DedupEntry(nil), a.seenFifo...),
		State: a.host.Snapshot(),
	})
	waitForRefs(t, st, "chunk-ladder", 1)

	// The replay tail the checkpoint does not cover: more traffic and
	// some idle (watermark-only) ticks.
	for i := 0; i < 12; i++ {
		a.stageIntent(s, uint64(100+i), kMove, move(int32(i%5-2)))
		a.tickOnce()
	}
	for i := 0; i < 8; i++ {
		a.tickOnce()
	}
	tip := ticklog.Tip{Tick: a.host.Tick() - 1, Seq: a.lastSeq}
	a.close()
	a.host.close()
	return builtWorld{dir: dir, st: st, module: module, cw: cw, pw: pw, pgTip: tip}
}

func waitForRefs(t *testing.T, st checkpoint.Store, chunk string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		refs, err := st.List(chunk)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkpoint never landed (%d/%d)", len(refs), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func acquireFor(t *testing.T, dir string) *ticklog.Guard {
	t.Helper()
	g, err := ticklog.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Release() })
	return g
}

func TestRecoverCleanReplay(t *testing.T) {
	w := buildShadowedWorld(t)
	g := acquireFor(t, w.dir)
	states, err := recoverWorld(g, w.st, w.dir, w.cw, w.pw, []string{"chunk-ladder"})
	if err != nil {
		t.Fatal(err)
	}
	b := states["chunk-ladder"]
	if b.ckpt.Version == 0 {
		t.Fatal("ladder returned genesis for a lived world")
	}
	if b.rewound || b.lineage != 0 {
		t.Fatalf("clean tail must not rewind (rewound=%v lineage=%d)", b.rewound, b.lineage)
	}
	if len(b.runs) == 0 {
		t.Fatal("no replay material past the checkpoint")
	}
	// Every event must be recovered (seq is the claim); trailing idle
	// ticks may trail the journal by the watermark coalescing window.
	if b.tip.Seq != w.pgTip.Seq {
		t.Fatalf("recovered seq %d, journal seq %d — the shadow window's whole claim", b.tip.Seq, w.pgTip.Seq)
	}
	if b.tip.Tick > w.pgTip.Tick {
		t.Fatalf("recovered tip tick %d past the journal's %d", b.tip.Tick, w.pgTip.Tick)
	}
	if err := proveAndReplay(w.module, nil, b); err != nil {
		t.Fatalf("proof: %v", err)
	}
	if err := w.st.MarkProven(b.ref); err != nil {
		t.Fatal(err)
	}
	refs, _ := w.st.List("chunk-ladder")
	if len(refs) == 0 || !refs[0].Proven {
		t.Fatal("proof did not mark the ref proven")
	}
}

func TestRecoverTornTailTruncates(t *testing.T) {
	w := buildShadowedWorld(t)
	shearNewestSegment(t, w.dir, 7)
	g := acquireFor(t, w.dir)
	states, err := recoverWorld(g, w.st, w.dir, w.cw, w.pw, []string{"chunk-ladder"})
	if err != nil {
		t.Fatalf("a torn tail must recover, not refuse: %v", err)
	}
	b := states["chunk-ladder"]
	if b.rewound {
		t.Fatal("a torn tail is below the loss floor — no rewind")
	}
	if b.tip.Seq >= w.pgTip.Seq && b.tip.Tick >= w.pgTip.Tick {
		t.Fatalf("shear lost nothing? tip %+v vs journal %+v", b.tip, w.pgTip)
	}
	if err := proveAndReplay(w.module, nil, b); err != nil {
		t.Fatalf("proof after truncation: %v", err)
	}
}

func TestRecoverCorruptTailFallsToCheckpointOnly(t *testing.T) {
	w := buildShadowedWorld(t)
	corruptNewestSegmentMiddle(t, w.dir)
	g := acquireFor(t, w.dir)
	states, err := recoverWorld(g, w.st, w.dir, w.cw, w.pw, []string{"chunk-ladder"})
	if err != nil {
		t.Fatalf("damage-with-survivors falls to the checkpoint rung, not an error: %v", err)
	}
	b := states["chunk-ladder"]
	if !b.rewound || b.lineage != 1 {
		t.Fatalf("corrupt WAL must rewind lineage (rewound=%v lineage=%d)", b.rewound, b.lineage)
	}
	if len(b.runs) != 0 {
		t.Fatal("a refused WAL must contribute no replay material")
	}
	if b.tip.Seq != b.ckpt.Seq || b.tip.Tick != b.ckpt.Tick {
		t.Fatalf("checkpoint-only rung must resume at the manifest (%+v vs seq=%d tick=%d)", b.tip, b.ckpt.Seq, b.ckpt.Tick)
	}
	if err := proveAndReplay(w.module, nil, b); err != nil {
		t.Fatalf("checkpoint proof: %v", err)
	}
}

func TestRecoverPairMismatchRefuses(t *testing.T) {
	w := buildShadowedWorld(t)
	g := acquireFor(t, w.dir)
	otherPW := w.pw
	otherPW[0] ^= 0xFF
	_, err := recoverWorld(g, w.st, w.dir, w.cw, otherPW, []string{"chunk-ladder"})
	if err == nil || !strings.Contains(err.Error(), "module pair") {
		t.Fatalf("a volume whose checkpoints all mismatch the mounted pair must refuse, got %v", err)
	}
}

func TestRecoverGenesisOnlyWhenBlank(t *testing.T) {
	dir := t.TempDir()
	st, err := checkpoint.NewDir(filepath.Join(dir, "checkpoints"), 0)
	if err != nil {
		t.Fatal(err)
	}
	g := acquireFor(t, dir)
	states, err := recoverWorld(g, st, dir, [32]byte{}, [32]byte{}, []string{"chunk-new"})
	if err != nil {
		t.Fatal(err)
	}
	if b := states["chunk-new"]; b.ckpt.Version != 0 {
		t.Fatal("a blank volume is the one legal genesis")
	}
}

func TestRecoverRefusesHistoryWithoutCheckpoint(t *testing.T) {
	w := buildShadowedWorld(t)
	// Same WAL, empty store: as if the checkpoint directory was lost.
	empty, err := checkpoint.NewDir(filepath.Join(t.TempDir(), "checkpoints"), 0)
	if err != nil {
		t.Fatal(err)
	}
	g := acquireFor(t, w.dir)
	// Either refusal is legal and loud: the genesis probe scans from seq
	// 0 and a shadow-era WAL provably does not start there (ErrGap), or
	// a post-flip WAL that does reach the explicit history check.
	_, err = recoverWorld(g, empty, w.dir, w.cw, w.pw, []string{"chunk-ladder"})
	if err == nil || !(strings.Contains(err.Error(), "without a restorable checkpoint") || strings.Contains(err.Error(), "seq gap")) {
		t.Fatalf("WAL history with no checkpoint must refuse to re-genesis, got %v", err)
	}
}

// TestRehearsalProvesOnReopen is the boot-time rehearsal end to end: a
// second activation on the same volume runs the ladder between the
// writer lock and its fresh Create, and a passing proof leaves the
// chosen ref marked proven.
func TestRehearsalProvesOnReopen(t *testing.T) {
	w := buildShadowedWorld(t)
	wal := newShadowFactory(w.dir, w.st, "toy")("chunk-ladder", 1, w.pgTip.Tick+1, w.pgTip.Seq, shadowBoot{module: w.module, clientSum: w.cw})
	if wal == nil {
		t.Fatal("second activation failed to open")
	}
	defer wal.close()
	refs, err := w.st.List("chunk-ladder")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 || !refs[0].Proven {
		t.Fatalf("rehearsal did not prove the recovered checkpoint: %+v", refs)
	}
}

// segmentPaths returns the WAL's segment files, oldest first.
func segmentPaths(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".chkw") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("no WAL segments on the volume")
	}
	return out
}

// shearNewestSegment cuts n bytes off the newest segment — the on-disk
// shape of a crash mid-write (the tail reads as torn, not corrupt,
// because nothing valid follows the cut).
func shearNewestSegment(t *testing.T, dir string, n int64) {
	t.Helper()
	paths := segmentPaths(t, dir)
	p := paths[len(paths)-1]
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, info.Size()-n); err != nil {
		t.Fatal(err)
	}
}

// corruptNewestSegmentMiddle stamps garbage into the middle of the
// newest segment, leaving valid records beyond it — damage with
// survivors, the shape that must refuse rather than splice.
func corruptNewestSegmentMiddle(t *testing.T, dir string) {
	t.Helper()
	paths := segmentPaths(t, dir)
	p := paths[len(paths)-1]
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x55, 0xAA}, info.Size()*3/5); err != nil {
		t.Fatal(err)
	}
}
