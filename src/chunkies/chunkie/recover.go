package chunkie

// The recovery ladder — §5 of the durability design, rungs 2–6 for one
// activation. Rung 1 (the writer lock) has already run: the Guard is a
// parameter, not a step, so the ladder is unreachable without it.
//
// Refusal philosophy inherited from ticklog and the checkpoint store,
// loud by design: a seq gap (ErrGap) and damage with valid records
// beyond it (ErrCorrupt after the intact prefix) refuse the WAL; a
// chunk whose WAL has history but whose checkpoints are all unreadable
// or all mismatch the mounted module pair refuses the chunk. Genesis is
// returned only for a chunk with no trace on the volume — the caller
// must alert on genesis-where-history-was-expected.
//
// During the shadow window this runs as a boot-time rehearsal while PG
// remains the recovery authority; the flip makes it the serving path.

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/checkpoint"
	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// bootState is one chunk's recovered position: the checkpoint to
// restore, the replay material past it, and the identity the next
// activation serves under.
type bootState struct {
	ckpt codec.Checkpoint // Version 0 => genesis (the loud path)
	ref  checkpoint.Ref   // the manifest's ref, for MarkProven after proof
	runs []codec.Record   // event-carrying tick records past the checkpoint
	tip  ticklog.Tip      // replay target from the WAL scan
	// lineage is the history the activation serves under; bumped iff
	// acked ticks were lost (rung 5) — a wire-visible void: clients
	// resync, never splice a false world.
	lineage uint32
	rewound bool
}

// recoverWorld walks the ladder for every chunk the deployment serves.
// cw/pw are the mounted module pair; a checkpoint under any other pair
// is skipped (a crash between promotion steps 2 and 3 leaves a newer
// manifest under the pair that never mounted — the mounted pair resumes
// its own newest, Figure 3), and a chunk with checkpoints but none
// under the mounted pair refuses: that is the wrong module for this
// volume, corruption-class, never compatibility.
func recoverWorld(g *ticklog.Guard, st checkpoint.Store, walDir string, cw, pw [32]byte, chunks []string) (map[string]bootState, error) {
	if g == nil {
		return nil, errors.New("recovery ladder without the writer lock")
	}
	out := make(map[string]bootState, len(chunks))
	keys := make([]ticklog.ChunkKey, 0, len(chunks))
	maxLineage := make(map[string]uint32, len(chunks))
	for _, name := range chunks {
		refs, err := st.List(name)
		if err != nil {
			return nil, fmt.Errorf("chunk %s: list checkpoints: %w", name, err)
		}
		var chosen *checkpoint.Ref
		var m codec.Checkpoint
		mismatched := 0
		for i := range refs {
			r := refs[i]
			if r.Lineage > maxLineage[name] {
				maxLineage[name] = r.Lineage
			}
			c, err := st.Load(r)
			if err != nil {
				// A named manifest failing integrity is disk damage (a
				// torn write never gets a name) — the ring's older
				// survivor is the recovery, not a refusal: WAL trim
				// floors at the oldest kept checkpoint, so replay still
				// reaches the tip from the survivor.
				log.Printf("chunk %s: checkpoint lineage=%d seq=%d failed integrity (%v) — trying older", name, r.Lineage, r.Seq, err)
				continue
			}
			if c.CW != cw || c.PW != pw {
				mismatched++
				continue
			}
			chosen, m = &refs[i], c
			break
		}
		if chosen == nil {
			if mismatched > 0 {
				return nil, fmt.Errorf("chunk %s: no checkpoint matches the mounted module pair (%d under other pairs) — wrong module for this volume, refusing", name, mismatched)
			}
			if len(refs) > 0 {
				return nil, fmt.Errorf("chunk %s: all %d checkpoints failed integrity — refusing", name, len(refs))
			}
			// No checkpoints: genesis candidate. The scan below must come
			// back equally blank — WAL history without a restorable
			// checkpoint refuses rather than silently re-genesis a lived
			// world. (The blank-history probe scans lineage 0: every
			// pre-flip writer is lineage 0, and post-flip a chunk always
			// checkpoints before trim can erase its lineage evidence.)
			keys = append(keys, ticklog.ChunkKey{Name: name, Lineage: 0, AfterTick: 0, AfterSeq: -1})
			out[name] = bootState{}
			continue
		}
		out[name] = bootState{
			ckpt: m, ref: *chosen, lineage: m.Lineage,
			tip: ticklog.Tip{Tick: m.Tick, Seq: m.Seq},
		}
		keys = append(keys, ticklog.ChunkKey{Name: name, Lineage: m.Lineage, AfterTick: m.Tick, AfterSeq: m.Seq})
	}

	runs := make(map[string][]codec.Record, len(chunks))
	tips, scanErr := ticklog.Scan(walDir, keys, func(chunk string, r codec.Record) error {
		if r.Count == 0 {
			return nil // watermarks advance tips; no replay material
		}
		// Scan reuses its frame buffer between records: copy what we keep.
		r.Records = append([]byte(nil), r.Records...)
		runs[chunk] = append(runs[chunk], r)
		return nil
	})
	switch {
	case scanErr == nil:
		// Clean or torn tail. A torn tail was truncated at the shear —
		// bounded by the group commit (~20ms), below the loss floor, no
		// rewind.
	case errors.Is(scanErr, ticklog.ErrGap):
		return nil, fmt.Errorf("recovery: %w — a missing segment is never replayed around", scanErr)
	case errors.Is(scanErr, codec.ErrCorrupt):
		// Damage with survivors beyond it: refuse the WAL loudly and
		// fall to the checkpoint-only rung. Acked ticks are lost, so
		// every chunk on the volume serves a rewound lineage.
		for _, name := range chunks {
			b := out[name]
			if b.ckpt.Version == 0 {
				// A genesis verdict needs a readable WAL to prove "no
				// history"; a corrupt one can be hiding it.
				return nil, fmt.Errorf("chunk %s: no checkpoint and the WAL is corrupt — cannot rule out lost history: %w", name, scanErr)
			}
			b.runs = nil
			b.tip = ticklog.Tip{Tick: b.ckpt.Tick, Seq: b.ckpt.Seq}
			b.lineage = maxLineage[name] + 1
			b.rewound = true
			out[name] = b
		}
		log.Printf("recovery: WAL refused (%v) — checkpoint-only rung, lineage rewound", scanErr)
		return out, nil
	default:
		return nil, fmt.Errorf("recovery scan: %w", scanErr)
	}
	for _, name := range chunks {
		b := out[name]
		if b.ckpt.Version == 0 {
			if tip := tips[name]; len(runs[name]) > 0 || tip.Seq > -1 || tip.Tick > 0 {
				return nil, fmt.Errorf("chunk %s: WAL history without a restorable checkpoint — refusing to re-genesis over a lived world", name)
			}
			continue // true genesis: no trace on the volume
		}
		b.runs = runs[name]
		b.tip = tips[name]
		out[name] = b
	}
	return out, nil
}

// errContentSwap marks a replay window that crosses a terrain swap; the
// rehearsal path reports it as skipped rather than failed (fetching
// mid-replay blobs is the serving ladder's job at the flip, and the PG
// boot path already proves that choreography today).
var errContentSwap = errors.New("recovery replay crosses a content swap")

// proveAndReplay is the expensive verb of the tiered verification:
// restore the checkpoint into a fresh instance, demand the manifest's
// world hash, then replay the WAL runs through the same apply path the
// live tick uses and demand every tick record's post-step hash. The
// throwaway host is closed before return; the caller owns MarkProven.
func proveAndReplay(module []byte, terrain []byte, b bootState) error {
	if b.ckpt.Version == 0 {
		return errors.New("nothing to prove: genesis bootState")
	}
	host, err := newSimHost(module)
	if err != nil {
		return err
	}
	defer host.close()
	if b.ckpt.Content != 0 {
		if terrainID(terrain) != b.ckpt.Content {
			// The active blob moved past the checkpoint's content — the
			// serving ladder fetches by id; the rehearsal only has the
			// current blob in hand.
			return fmt.Errorf("%w: checkpoint content %016x, active %016x", errContentSwap, b.ckpt.Content, terrainID(terrain))
		}
		if err := host.SetTerrain(terrain); err != nil {
			return err
		}
	}
	state, err := checkpoint.Inflate(b.ckpt.State)
	if err != nil {
		return fmt.Errorf("checkpoint state: %w", err)
	}
	if err := host.Restore(state); err != nil {
		return err
	}
	if got := host.Hash(); got != b.ckpt.WH {
		return fmt.Errorf("restore hash %016x != manifest %016x — refusing", got, b.ckpt.WH)
	}
	for _, r := range b.runs {
		recs, err := codec.ParseRecords(r.Records, int(r.Count))
		if err != nil {
			return fmt.Errorf("replay tick %d: %w", r.Tick, err)
		}
		for host.Tick() < r.Tick {
			host.Step()
		}
		for _, rec := range recs {
			if rec.Kind == codec.KindContentSet {
				return errContentSwap
			}
			if code := host.Apply(rec.SimEvent); code != 0 {
				return fmt.Errorf("replay: tick %d kind %d rejected with code %d", r.Tick, rec.Kind, code)
			}
		}
		host.Step()
		if got := host.Hash(); got != r.WH {
			return fmt.Errorf("replay diverged at tick %d: %016x != %016x", r.Tick, got, r.WH)
		}
	}
	// Watermarks past the last event-carrying record are idle ticks;
	// step through them so the proven world stands at the tip.
	for host.Tick() <= b.tip.Tick {
		host.Step()
	}
	return nil
}

// rehearse runs the full ladder against the volume between the writer
// lock and the fresh activation's Create — the shadow window's dress
// rehearsal for the flip. Outcomes are metrics and logs, never serving
// decisions: PG remains the recovery authority. lossTicks is how far
// the volume's proven tip trails the PG tip the activation is about to
// shadow from — the crash drill's "≤20ms loss" graph.
func rehearse(g *ticklog.Guard, st checkpoint.Store, walDir, chunk string, module, terrain []byte, cw, pw [32]byte, pgTip ticklog.Tip) {
	start := time.Now()
	states, err := recoverWorld(g, st, walDir, cw, pw, []string{chunk})
	if err != nil {
		mRehearsals.WithLabelValues("refused").Inc()
		log.Printf("chunk %s: recovery rehearsal refused: %v", chunk, err)
		return
	}
	b := states[chunk]
	if b.ckpt.Version == 0 {
		// Expected on a young volume (first activation, or the cadence
		// has not fired yet). The flip's serving ladder alerts here.
		mRehearsals.WithLabelValues("genesis").Inc()
		log.Printf("chunk %s: recovery rehearsal: no history on volume (genesis rung)", chunk)
		return
	}
	if err := proveAndReplay(module, terrain, b); err != nil {
		if errors.Is(err, errContentSwap) {
			mRehearsals.WithLabelValues("content_swap_skipped").Inc()
			log.Printf("chunk %s: recovery rehearsal skipped: %v", chunk, err)
			return
		}
		mRehearsals.WithLabelValues("failed").Inc()
		log.Printf("chunk %s: RECOVERY REHEARSAL FAILED: %v — the volume would not recover this world; PG remains authority, investigate before the flip", chunk, err)
		return
	}
	if err := st.MarkProven(b.ref); err != nil {
		log.Printf("chunk %s: mark proven: %v", chunk, err)
	}
	outcome := "clean"
	if b.rewound {
		outcome = "checkpoint_only"
	}
	loss := int64(pgTip.Tick) - int64(b.tip.Tick)
	mRehearsals.WithLabelValues(outcome).Inc()
	mRehearsalLossTicks.Set(float64(loss))
	log.Printf("chunk %s: recovery rehearsal %s in %s: recovered lineage=%d tip tick=%d seq=%d (PG tip tick=%d seq=%d, loss=%d ticks)",
		chunk, outcome, time.Since(start).Round(time.Millisecond), b.lineage, b.tip.Tick, b.tip.Seq, pgTip.Tick, pgTip.Seq, loss)
}
