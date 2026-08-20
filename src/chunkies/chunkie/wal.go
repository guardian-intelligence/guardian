package chunkie

// Slice B of the durability rewrite: the ticklog engine rides beside the
// Postgres journal, mirroring every seq the journal assigns, while PG
// remains the recovery authority. The shadow's failure policy is the
// whole point of the mode: a fault latches the shadow dead and counts —
// it never touches the serving path. The flip makes this wiring
// authoritative by swapping that policy, not the call sites.

import (
	"context"
	"crypto/sha256"
	"log"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/checkpoint"
	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// shadowBoot is the activation-time material only the caller has in
// hand: the running sim module and active terrain (the rehearsal's
// proof inputs) and the distributed client module's identity (half the
// checkpoint manifest's pair pin).
type shadowBoot struct {
	module    []byte
	terrain   []byte
	clientSum [32]byte
}

// shadowFunc opens the shadow for one chunk activation at its exact
// journal tip; nil disables the shadow entirely (dark authorities,
// tests, prod before the volume lands).
type shadowFunc func(chunk string, epoch uint32, nextTick uint64, lastSeq int64, boot shadowBoot) *shadowWAL

// newShadowFactory wires one activation volume directory. Each call of
// the returned shadowFunc is one activation: it takes the writer lock,
// rehearses the recovery ladder against whatever the volume holds
// (slice C: PG is still the authority — a failed rehearsal costs a
// graph, not a world), mints a generation, and creates a fresh log plus
// the checkpoint cadence lane; the authority's close releases all of
// it, so an authority reopen is a new activation by construction.
// st nil disables the checkpoint lane and the rehearsal (WAL-only,
// slice B's shape).
func newShadowFactory(dir string, st checkpoint.Store, game string) shadowFunc {
	return func(chunk string, epoch uint32, nextTick uint64, lastSeq int64, boot shadowBoot) *shadowWAL {
		guard, err := ticklog.Acquire(dir)
		if err != nil {
			// Shadow policy: the world serves without its shadow rather
			// than not at all. ErrHeld here means a predecessor's process
			// is still dying; the next reopen acquires cleanly. Serving
			// unshadowed IS the dead state — the gauge must say so.
			log.Printf("chunk %s: shadow WAL acquire: %v — serving unshadowed", chunk, err)
			mWALFaults.Inc()
			mWALShadowDead.Set(1)
			return nil
		}
		if st != nil {
			// Between the lock and Create: the volume is quiescent and
			// fenced, exactly what the ladder will see at the flip.
			pw := sha256.Sum256(boot.module)
			rehearse(guard, st, dir, chunk, boot.module, boot.terrain, boot.clientSum, pw, ticklog.Tip{Tick: nextTick, Seq: lastSeq})
		}
		l, err := ticklog.Create(ticklog.Config{
			Dir:        dir,
			Generation: guard.Generation(),
			Chunks: []ticklog.ChunkSpec{{
				Name: chunk, Lineage: 0, Epoch: epoch,
				StartTick: nextTick, StartSeq: lastSeq,
			}},
		})
		if err != nil {
			log.Printf("chunk %s: shadow WAL create: %v — serving unshadowed", chunk, err)
			mWALFaults.Inc()
			mWALShadowDead.Set(1)
			guard.Release()
			return nil
		}
		w := &shadowWAL{guard: guard, log: l, game: game, stop: make(chan struct{})}
		if st != nil {
			w.ckpt = checkpoint.New(st, l, checkpoint.Config{})
		}
		mWALShadowDead.Set(0)
		go w.watch(chunk)
		return w
	}
}

type shadowWAL struct {
	guard *ticklog.Guard
	log   *ticklog.Log
	ckpt  *checkpoint.Snapshotter // nil in WAL-only wiring
	game  string
	dead  atomic.Bool
	stop  chan struct{}
}

// watch exports the durable-lag gauge and consumes the engine's fault
// channel. The gauge is the disk half of tick-stall attribution (the
// network half is the trunk shed counter).
func (w *shadowWAL) watch(chunk string) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case err := <-w.log.Faults():
			w.latch(chunk, err)
		case <-t.C:
			// A poisoned engine's oldest-unsynced age freezes, so its
			// DurableLag climbs forever: once dead, the lag is not a
			// disk signal any more — the dead gauge carries the state.
			if w.dead.Load() {
				mWALLag.Set(0)
			} else {
				mWALLag.Set(w.log.DurableLag().Seconds())
			}
		case <-w.stop:
			mWALLag.Set(0)
			return
		}
	}
}

// latch is the shadow fault policy: count, log once, stop appending.
// Serving is untouched — PG is still the recovery authority.
func (w *shadowWAL) latch(chunk string, err error) {
	if w.dead.Swap(true) {
		return
	}
	mWALFaults.Inc()
	mWALShadowDead.Set(1)
	log.Printf("chunk %s: shadow WAL dead: %v (serving continues; PG remains authority)", chunk, err)
}

func (w *shadowWAL) appendTick(chunk string, tick uint64, firstSeq int64, count uint16, run []byte, wh uint64) {
	if w == nil || w.dead.Load() {
		return
	}
	if err := w.log.AppendTick(0, tick, firstSeq, count, run, wh); err != nil {
		w.latch(chunk, err)
	}
}

func (w *shadowWAL) watermark(tick uint64) {
	if w == nil || w.dead.Load() {
		return
	}
	w.log.Watermark(0, tick)
}

// dueCheckpoint gates the tick loop's only checkpoint cost: when it
// returns true the caller pays one sim_snapshot and hands the bytes to
// submitCheckpoint; everything else happens on the snapshotter's lane.
// A dead shadow stops checkpointing too — its WAL can no longer cover
// the gap between checkpoints, so a fresher checkpoint would narrate a
// history the volume cannot replay.
func (w *shadowWAL) dueCheckpoint(chunk string, now time.Time) bool {
	return w != nil && w.ckpt != nil && !w.dead.Load() && w.ckpt.Due(chunk, now)
}

func (w *shadowWAL) submitCheckpoint(m codec.Checkpoint) {
	if w == nil || w.ckpt == nil || w.dead.Load() {
		return
	}
	m.Generation = w.guard.Generation()
	w.ckpt.Submit(m)
}

// advanceEpoch is the epoch barrier: no segment ever spans a module
// promotion. Synchronous by design — the swap path is already heavy.
func (w *shadowWAL) advanceEpoch(chunk string, epoch uint32) {
	if w == nil || w.dead.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.log.AdvanceEpoch(ctx, 0, epoch); err != nil {
		w.latch(chunk, err)
	}
}

func (w *shadowWAL) close() {
	if w == nil {
		return
	}
	w.dead.Store(true)
	close(w.stop)
	if w.ckpt != nil {
		// The snapshotter first: its retention pass trims through the
		// log, which must still be open under it.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := w.ckpt.Close(ctx); err != nil {
			log.Printf("checkpoint lane close: %v", err)
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := w.log.Barrier(ctx); err != nil {
		log.Printf("shadow WAL close barrier: %v", err)
	}
	cancel()
	w.log.Close()
	w.guard.Release()
}
