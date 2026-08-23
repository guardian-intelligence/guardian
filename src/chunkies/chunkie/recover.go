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
// cw/pw are the mounted module pair. Selection keys on PW alone — the
// sim module is the only replay input: a checkpoint under another sim
// module is skipped (a crash between promotion steps 2 and 3 leaves a
// newer manifest under the module that never mounted — the mounted one
// resumes its own newest, Figure 3), and a chunk with checkpoints but
// none under the mounted sim module refuses: wrong module for this
// volume, corruption-class, never compatibility. CW records the client
// bundle for forensics; its drift is counted and logged, never refused
// (the client hot-swaps with no tick barrier, so old-CW manifests are a
// routine deploy's shape, not damage).
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
			if c.PW != pw {
				mismatched++
				continue
			}
			if c.CW != cw {
				// Only the sim module (PW) decides replay determinism — the
				// client module never touches world state, and it hot-swaps
				// on the mount with no tick barrier, so a routine client
				// deploy legitimately leaves every manifest old-CW for up
				// to a cadence. Drift is observable, never refusable.
				mRecoveryCWDrift.Inc()
				log.Printf("chunk %s: checkpoint lineage=%d seq=%d carries CW %x (mounted %x) — client-module drift, recovering anyway", name, r.Lineage, r.Seq, c.CW[:4], cw[:4])
			}
			chosen, m = &refs[i], c
			break
		}
		if chosen == nil {
			if mismatched > 0 {
				return nil, fmt.Errorf("chunk %s: no checkpoint matches the mounted sim module (%d under other modules) — wrong module for this volume, refusing", name, mismatched)
			}
			if len(refs) > 0 {
				return nil, fmt.Errorf("chunk %s: all %d checkpoints failed integrity — refusing", name, len(refs))
			}
			// No checkpoints: genesis candidate. Two probes must both come
			// back blank — WAL history without a restorable checkpoint
			// refuses rather than silently re-genesis a lived world. The
			// record-level scan below covers lineage 0; the header-level
			// trace sweep covers what a lineage-0 scan cannot see (a
			// rewound world whose lineage-0 segments were trimmed and
			// whose checkpoint directory was then lost).
			trace, terr := ticklog.HasTrace(walDir, name)
			if terr != nil {
				return nil, fmt.Errorf("chunk %s: probing the volume for history: %w", name, terr)
			}
			if trace {
				return nil, fmt.Errorf("chunk %s: %w — segments name a lived world, refusing to re-genesis", name, errUncheckpointed)
			}
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
				return nil, fmt.Errorf("chunk %s: %w — refusing to re-genesis over a lived world", name, errUncheckpointed)
			}
			continue // true genesis: no trace on the volume
		}
		b.runs = runs[name]
		b.tip = tips[name]
		out[name] = b
	}
	return out, nil
}

// errUncheckpointed marks a volume that carries WAL history for a lived
// world but no restorable checkpoint. Post-flip this refuses serving;
// during the shadow window the rehearsal reports it as its own outcome,
// because it is the guaranteed shape of the first activations after the
// checkpoint lane is enabled over an already-shadowing volume — the
// segments exist, the first cadence has not fired — and calling that
// "refused" would poison the drill gate's metric with a non-event.
var errUncheckpointed = errors.New("WAL history without a restorable checkpoint")

// errContentSwap marks a replay window that crosses a terrain swap; the
// rehearsal path reports it as skipped rather than failed (fetching
// mid-replay blobs is the serving ladder's job at the flip, and the PG
// boot path already proves that choreography today).
var errContentSwap = errors.New("recovery replay crosses a content swap")

// proveRestore is the restore half of the ladder's proof: a fresh
// instance of module, the terrain if any, the raw state, the manifest's
// world hash. E-variants throughout — a trap here is a failed proof and
// must surface as one, never as a panic in the serving process. On
// success the proven host is returned live for the caller to replay
// into (or close immediately).
func proveRestore(module, terrain, state []byte, wh uint64) (*simHost, error) {
	host, err := newSimHost(module)
	if err != nil {
		return nil, err
	}
	if len(terrain) > 0 {
		if err := host.SetTerrainE(terrain); err != nil {
			host.close()
			return nil, err
		}
	}
	if err := host.RestoreE(state); err != nil {
		host.close()
		return nil, err
	}
	got, err := host.HashE()
	if err != nil {
		host.close()
		return nil, err
	}
	if got != wh {
		host.close()
		return nil, fmt.Errorf("restore hash %016x != manifest %016x — refusing", got, wh)
	}
	return host, nil
}

// proveAndReplay is the expensive verb of the tiered verification:
// restore the checkpoint into a fresh instance, demand the manifest's
// world hash, then replay the WAL runs through the same apply path the
// live tick uses and demand every tick record's post-step hash. The
// throwaway host is closed before return; the caller owns MarkProven.
// E-variants throughout: rehearsal outcomes are metrics and logs, and
// a state that traps the module is a failed rehearsal, not a dead
// chunkie in a deterministic crash loop.
func proveAndReplay(module []byte, terrain []byte, b bootState) error {
	if b.ckpt.Version == 0 {
		return errors.New("nothing to prove: genesis bootState")
	}
	if b.ckpt.Content != 0 {
		if terrainID(terrain) != b.ckpt.Content {
			// The active blob moved past the checkpoint's content — the
			// serving ladder fetches by id; the rehearsal only has the
			// current blob in hand.
			return fmt.Errorf("%w: checkpoint content %016x, active %016x", errContentSwap, b.ckpt.Content, terrainID(terrain))
		}
	} else {
		terrain = nil
	}
	state, err := checkpoint.Inflate(b.ckpt.State)
	if err != nil {
		return fmt.Errorf("checkpoint state: %w", err)
	}
	host, err := proveRestore(module, terrain, state, b.ckpt.WH)
	if err != nil {
		return err
	}
	defer host.close()
	for _, r := range b.runs {
		recs, err := codec.ParseRecords(r.Records, int(r.Count))
		if err != nil {
			return fmt.Errorf("replay tick %d: %w", r.Tick, err)
		}
		for {
			tk, err := host.TickE()
			if err != nil {
				return err
			}
			if tk >= r.Tick {
				break
			}
			if err := host.StepE(); err != nil {
				return err
			}
		}
		for _, rec := range recs {
			if rec.Kind == codec.KindContentSet {
				return errContentSwap
			}
			code, err := host.ApplyE(rec.SimEvent)
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("replay: tick %d kind %d rejected with code %d", r.Tick, rec.Kind, code)
			}
		}
		if err := host.StepE(); err != nil {
			return err
		}
		got, err := host.HashE()
		if err != nil {
			return err
		}
		if got != r.WH {
			return fmt.Errorf("replay diverged at tick %d: %016x != %016x", r.Tick, got, r.WH)
		}
	}
	// Watermarks past the last event-carrying record are idle ticks;
	// step through them so the proven world stands at the tip.
	for {
		tk, err := host.TickE()
		if err != nil {
			return err
		}
		if tk > b.tip.Tick {
			break
		}
		if err := host.StepE(); err != nil {
			return err
		}
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
		if errors.Is(err, errUncheckpointed) {
			// The first activations after enabling the checkpoint lane
			// land here by construction: segments exist, no cadence has
			// fired. Post-flip the serving ladder still refuses this
			// shape — an uncheckpointed volume cannot recover its world.
			mRehearsals.WithLabelValues("uncheckpointed").Inc()
			log.Printf("chunk %s: recovery rehearsal: %v (expected until the first cadence checkpoint lands)", chunk, err)
			return
		}
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
