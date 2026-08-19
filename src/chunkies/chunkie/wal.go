package chunkie

// Slice B of the durability rewrite: the ticklog engine rides beside the
// Postgres journal, mirroring every seq the journal assigns, while PG
// remains the recovery authority. The shadow's failure policy is the
// whole point of the mode: a fault latches the shadow dead and counts —
// it never touches the serving path. The flip makes this wiring
// authoritative by swapping that policy, not the call sites.

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
)

// shadowFunc opens the shadow for one chunk activation at its exact
// journal tip; nil disables the shadow entirely (dark authorities,
// tests, prod before the volume lands).
type shadowFunc func(chunk string, epoch uint32, nextTick uint64, lastSeq int64) *shadowWAL

// newShadowFactory wires one activation volume directory. Each call of
// the returned shadowFunc is one activation: it takes the writer lock,
// mints a generation, and creates a fresh log; the authority's close
// releases both, so an authority reopen is a new activation by
// construction.
func newShadowFactory(dir string) shadowFunc {
	return func(chunk string, epoch uint32, nextTick uint64, lastSeq int64) *shadowWAL {
		guard, err := ticklog.Acquire(dir)
		if err != nil {
			// Shadow policy: the world serves without its shadow rather
			// than not at all. ErrHeld here means a predecessor's process
			// is still dying; the next reopen acquires cleanly.
			log.Printf("chunk %s: shadow WAL acquire: %v — serving unshadowed", chunk, err)
			mWALFaults.Inc()
			return nil
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
			guard.Release()
			return nil
		}
		w := &shadowWAL{guard: guard, log: l, stop: make(chan struct{})}
		mWALShadowDead.Set(0)
		go w.watch(chunk)
		return w
	}
}

type shadowWAL struct {
	guard *ticklog.Guard
	log   *ticklog.Log
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
			mWALLag.Set(w.log.DurableLag().Seconds())
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := w.log.Barrier(ctx); err != nil {
		log.Printf("shadow WAL close barrier: %v", err)
	}
	cancel()
	w.log.Close()
	w.guard.Release()
}
