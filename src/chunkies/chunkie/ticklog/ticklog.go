// Package ticklog is the chunkie's write-ahead engine: it accepts each
// chunk's tick record without blocking its loop, makes the set durable on
// one bounded cadence, and hands recovery a verified, ordered, per-chunk
// replay. One interleaved log serves every chunk in the pod, so one group
// commit covers all of them at any chunk count.
//
// The failure contract never lies about the loss bound: a full queue, a
// durable lag past the ceiling, or a failed sync latches a fault and every
// later call returns it — the chunkie fences and closes rather than
// silently widening the configured window. A failed sync poisons the file
// permanently; the engine never writes after one.
package ticklog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

var (
	// ErrStalled reports the engine wedged: the queue filled or the
	// durable lag exceeded its ceiling. The log is dead; fence and close.
	ErrStalled = errors.New("ticklog stalled")
	// ErrGap reports a seq discontinuity during replay: a segment is
	// missing. Recovery must refuse to open rather than replay around it.
	ErrGap = errors.New("ticklog seq gap")
)

// ChunkSpec declares one chunk of the activation. Records carry the
// chunk's index in Create's slice.
type ChunkSpec struct {
	Name    string
	Lineage uint32
	Epoch   uint32
	// StartTick is the first tick this activation may append — the
	// recovery ladder's replay tip plus one.
	StartTick uint64
	// StartSeq is the last seq already durable for the chunk; the first
	// appended record must continue at StartSeq+1.
	StartSeq int64
}

// Config configures one activation's log.
type Config struct {
	Dir string
	// Generation is minted per activation; the engine only ever creates
	// fresh segments stamped with it — a writer never appends to a prior
	// generation's file, so "find the tail" complexity lives only in the
	// reader and a fenced writer's leftovers are recognizable.
	Generation uint32
	Chunks     []ChunkSpec
	// SyncInterval is the sync cadence and therefore the accepted loss
	// window on process crash. Default 20ms.
	SyncInterval time.Duration
	// WatermarkEvery bounds how often an idle chunk's watermark is
	// written. Losing one costs only replay time. Default 1s.
	WatermarkEvery time.Duration
	// SegmentBytes is the preallocated segment size. Default 64MiB.
	SegmentBytes int64
	// QueueDepth bounds records in flight to the syncer; past it the log
	// declares itself stalled. Default 4096.
	QueueDepth int
	// MaxDurableLag is the self-fence ceiling on the oldest unsynced
	// record's age. Default 250ms.
	MaxDurableLag time.Duration

	fs  fsi              // test seam; nil = the OS
	now func() time.Time // test seam; nil = time.Now
}

// Tip is a chunk's durable frontier.
type Tip struct {
	Tick uint64
	Seq  int64
}

type pending struct {
	buf     *[]byte // pooled encoded record; nil for barriers
	chunk   int
	tick    uint64
	lastSeq int64
	enq     int64         // nanos, for the lag watchdog
	barrier chan struct{} // non-nil: flush, sync, then close it
	epoch   uint32        // with barrier: rotate with this epoch for chunk
	rotate  bool
}

// Log is the engine. AppendTick and Watermark are safe for one goroutine
// per chunk (each chunk's tick loop); the other methods for any.
type Log struct {
	cfg   Config
	fs    fsi
	now   func() time.Time
	ch    chan pending
	pool  sync.Pool
	fault atomic.Pointer[faultBox]
	fch   chan error
	dying chan struct{} // closed by the first poison; wakes the syncer
	done  chan struct{}

	// Producer-side per-chunk append cursors, single-writer each.
	nextTick []atomic.Uint64
	nextSeq  []atomic.Int64
	wmark    []atomic.Uint64 // pending watermark tick+1; 0 = none

	// oldestEnq is the enqueue time of the oldest not-yet-durable record
	// (nanos), 0 when everything is durable. The watchdog reads it.
	oldestEnq atomic.Int64

	mu      sync.Mutex
	durable []Tip

	// Syncer-only state.
	seg        segfile
	segPath    string
	segOff     int64
	segSize    int64
	ordinal    uint32
	epochs     []uint32
	lastLogged []uint64 // highest tick made durable per chunk (records or watermarks)
	staged     []uint64 // highest tick written to the buffer per chunk
	lastWM     []int64  // when each chunk's watermark was last written (nanos)
	lastSync   int64    // nanos
	unsynced   []pending
	wbuf       []byte
}

// Create opens a log for one activation: a fresh segment, never a reopen.
func Create(cfg Config) (*Log, error) {
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 20 * time.Millisecond
	}
	if cfg.WatermarkEvery <= 0 {
		cfg.WatermarkEvery = time.Second
	}
	if cfg.SegmentBytes <= 0 {
		cfg.SegmentBytes = 64 << 20
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 4096
	}
	if cfg.MaxDurableLag <= 0 {
		cfg.MaxDurableLag = 250 * time.Millisecond
	}
	if len(cfg.Chunks) == 0 || len(cfg.Chunks) > codec.MaxSegmentChunks {
		return nil, fmt.Errorf("ticklog: %d chunks (want 1..%d)", len(cfg.Chunks), codec.MaxSegmentChunks)
	}
	l := &Log{
		cfg:        cfg,
		fs:         cfg.fs,
		now:        cfg.now,
		ch:         make(chan pending, cfg.QueueDepth),
		fch:        make(chan error, 1),
		dying:      make(chan struct{}),
		done:       make(chan struct{}),
		nextTick:   make([]atomic.Uint64, len(cfg.Chunks)),
		nextSeq:    make([]atomic.Int64, len(cfg.Chunks)),
		wmark:      make([]atomic.Uint64, len(cfg.Chunks)),
		durable:    make([]Tip, len(cfg.Chunks)),
		epochs:     make([]uint32, len(cfg.Chunks)),
		lastLogged: make([]uint64, len(cfg.Chunks)),
		staged:     make([]uint64, len(cfg.Chunks)),
		lastWM:     make([]int64, len(cfg.Chunks)),
	}
	if l.fs == nil {
		l.fs = osFS{}
	}
	if l.now == nil {
		l.now = time.Now
	}
	l.pool.New = func() any { b := make([]byte, 0, 4096); return &b }
	for i, ch := range cfg.Chunks {
		l.nextTick[i].Store(ch.StartTick)
		l.nextSeq[i].Store(ch.StartSeq + 1)
		l.epochs[i] = ch.Epoch
		l.durable[i] = Tip{Tick: saturatingPrev(ch.StartTick), Seq: ch.StartSeq}
		l.lastLogged[i] = saturatingPrev(ch.StartTick)
		l.staged[i] = l.lastLogged[i]
	}
	if err := l.rotate(0); err != nil {
		return nil, err
	}
	l.lastSync = l.now().UnixNano()
	go l.run()
	go l.watchdog()
	return l, nil
}

func saturatingPrev(t uint64) uint64 {
	if t == 0 {
		return 0
	}
	return t - 1
}

// AppendTick hands the syncer one chunk's tick batch: the encoded
// EventRecord run the tick frame carries, plus the post-step world hash.
// It never blocks; a full queue is a fault, not a wait — dropping records
// or widening the window would make the configured loss bound fiction.
// Ticks must arrive strictly increasing per chunk, and firstSeq must
// continue the chunk's dense seq line.
func (l *Log) AppendTick(chunk int, tick uint64, firstSeq int64, count uint16, run []byte, wh uint64) error {
	if err := l.err(); err != nil {
		return err
	}
	if chunk < 0 || chunk >= len(l.nextTick) {
		return fmt.Errorf("ticklog: chunk %d out of range", chunk)
	}
	if count == 0 {
		return fmt.Errorf("ticklog: empty tick record (idle ticks log nothing)")
	}
	if tick < l.nextTick[chunk].Load() {
		return fmt.Errorf("ticklog: chunk %d tick %d not after %d", chunk, tick, l.nextTick[chunk].Load()-1)
	}
	if want := l.nextSeq[chunk].Load(); firstSeq != want {
		return fmt.Errorf("ticklog: chunk %d firstSeq %d breaks density (want %d)", chunk, firstSeq, want)
	}
	// A tick behind the chunk's own watermark would let the log claim
	// "idle through W" and then contradict itself; the tick loop drives
	// both calls, so this is its monotonicity bug surfacing.
	if w := l.wmark[chunk].Load(); tick < w {
		return fmt.Errorf("ticklog: chunk %d tick %d behind watermark %d", chunk, tick, w-1)
	}
	bp := l.pool.Get().(*[]byte)
	*bp = codec.AppendTickRecord((*bp)[:0], uint16(chunk), tick, firstSeq, count, run, wh)
	p := pending{
		buf: bp, chunk: chunk, tick: tick,
		lastSeq: firstSeq + int64(count) - 1,
		enq:     l.now().UnixNano(),
	}
	select {
	case l.ch <- p:
	default:
		l.poison(fmt.Errorf("%w: queue full", ErrStalled))
		return l.err()
	}
	l.nextTick[chunk].Store(tick + 1)
	l.nextSeq[chunk].Store(p.lastSeq + 1)
	return nil
}

// Watermark notes the tick an idle chunk has reached, bounding replay
// work. It costs one atomic store; the syncer writes at most one
// watermark per chunk per WatermarkEvery.
func (l *Log) Watermark(chunk int, tick uint64) {
	if chunk < 0 || chunk >= len(l.wmark) {
		return
	}
	// Single writer per chunk; the slot only ever advances.
	if cur := l.wmark[chunk].Load(); tick+1 > cur {
		l.wmark[chunk].Store(tick + 1)
	}
}

// Durable returns a chunk's durable frontier: everything at or before it
// survives a process crash.
func (l *Log) Durable(chunk int) Tip {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.durable[chunk]
}

// DurableLag returns the age of the oldest record not yet durable, zero
// when everything is. It is the number the wal-lag gauge should export.
func (l *Log) DurableLag() time.Duration {
	oldest := l.oldestEnq.Load()
	if oldest == 0 {
		return 0
	}
	return time.Duration(l.now().UnixNano() - oldest)
}

// Barrier makes everything appended so far durable before returning: the
// clean-close path, the checkpoint boundary, and the epoch barrier.
func (l *Log) Barrier(ctx context.Context) error {
	return l.barrier(ctx, -1, 0)
}

// AdvanceEpoch is the epoch barrier: it drains and syncs the log, then
// rotates to a fresh segment recording the chunk's new epoch — so no
// segment ever spans a module promotion, and replay always runs under
// exactly one module pair.
func (l *Log) AdvanceEpoch(ctx context.Context, chunk int, epoch uint32) error {
	if chunk < 0 || chunk >= len(l.nextTick) {
		return fmt.Errorf("ticklog: chunk %d out of range", chunk)
	}
	return l.barrier(ctx, chunk, epoch)
}

func (l *Log) barrier(ctx context.Context, chunk int, epoch uint32) error {
	if err := l.err(); err != nil {
		return err
	}
	done := make(chan struct{})
	p := pending{barrier: done, chunk: chunk, epoch: epoch, rotate: chunk >= 0, enq: l.now().UnixNano()}
	select {
	case l.ch <- p:
	case <-ctx.Done():
		return ctx.Err()
	case <-l.done:
		return l.err()
	}
	select {
	case <-done:
		return l.err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Faults delivers the first fault. The chunkie's reaction is fixed:
// fence, close, let the recovery ladder replay what is verifiably on
// disk.
func (l *Log) Faults() <-chan error { return l.fch }

// Close barriers and releases. After a fault it releases only. The close
// itself is not a fault: Faults never fires for it.
func (l *Log) Close() error {
	err := l.Barrier(context.Background())
	l.poison(errClosed)
	<-l.done
	return err
}

var errClosed = errors.New("ticklog closed")

type faultBox struct{ err error }

func (l *Log) err() error {
	if b := l.fault.Load(); b != nil {
		return b.err
	}
	return nil
}

func (l *Log) poison(err error) {
	if l.fault.CompareAndSwap(nil, &faultBox{err}) {
		close(l.dying)
		if err != errClosed {
			select {
			case l.fch <- err:
			default:
			}
		}
	}
}

// ---------- the syncer ----------

func (l *Log) run() {
	defer close(l.done)
	tick := time.NewTicker(l.cfg.SyncInterval)
	defer tick.Stop()
	var group []pending
	for {
		group = group[:0]
		select {
		case p := <-l.ch:
			group = append(group, p)
		case <-tick.C:
		case <-l.dying:
		}
	drain:
		for {
			select {
			case p := <-l.ch:
				group = append(group, p)
			default:
				break drain
			}
		}
		if l.commit(group) {
			return
		}
	}
}

// commit writes a drained group, syncs on cadence or barrier, publishes
// durability, and services rotations. It reports whether the log is done.
func (l *Log) commit(group []pending) (dead bool) {
	poisoned := l.err() != nil
	for _, p := range group {
		if poisoned {
			if p.buf != nil {
				l.pool.Put(p.buf)
			}
			if p.barrier != nil {
				close(p.barrier)
			}
			continue
		}
		if p.buf != nil {
			l.wbuf = append(l.wbuf, *p.buf...)
			l.pool.Put(p.buf)
			p.buf = nil
			l.staged[p.chunk] = p.tick
			l.unsynced = append(l.unsynced, p)
			l.oldestEnq.CompareAndSwap(0, p.enq)
			continue
		}
		// A barrier: everything before it becomes durable now.
		if err := l.flush(); err == nil && p.rotate {
			l.epochs[p.chunk] = p.epoch
			if rerr := l.rotate(0); rerr != nil {
				l.poison(rerr)
			}
		}
		poisoned = l.err() != nil
		close(p.barrier)
	}
	if poisoned {
		if l.seg != nil {
			l.seg.Close()
			l.seg = nil
		}
		return true
	}
	l.appendDueWatermarks()
	if len(l.wbuf) > 0 || len(l.unsynced) > 0 {
		if now := l.now().UnixNano(); now-l.lastSync >= int64(l.cfg.SyncInterval) {
			if err := l.flush(); err != nil {
				return true
			}
		}
	}
	return false
}

// appendDueWatermarks folds each idle chunk's latest noted tick into the
// write buffer, at most once per WatermarkEvery per chunk. A watermark
// must never outrun a still-queued tick record for its chunk — the log
// would claim "idle through W" and then contradict itself — so a chunk
// with records in flight defers to the next cycle.
func (l *Log) appendDueWatermarks() {
	now := l.now().UnixNano()
	for i := range l.wmark {
		w := l.wmark[i].Load()
		if w == 0 {
			continue
		}
		tick := w - 1
		if tick <= l.staged[i] || now-l.lastWM[i] < int64(l.cfg.WatermarkEvery) {
			continue
		}
		// nextTick is stored after the record is queued, so staged having
		// caught up means nothing is in flight; a racing append can only
		// add ticks at or past the watermark (AppendTick enforces it).
		if l.staged[i]+1 < l.nextTick[i].Load() {
			continue
		}
		l.wbuf = codec.AppendWatermark(l.wbuf, uint16(i), tick)
		l.lastWM[i] = now
		l.staged[i] = tick
		l.unsynced = append(l.unsynced, pending{chunk: i, tick: tick, lastSeq: -1, enq: now})
		l.oldestEnq.CompareAndSwap(0, now)
	}
}

// flush writes the buffered bytes and makes everything durable. On any
// error it poisons the log — after a failed sync, dirty pages may be
// gone, and a later clean sync proves nothing (the fsyncgate rule).
func (l *Log) flush() error {
	if len(l.wbuf) > 0 {
		if l.segOff+int64(len(l.wbuf)) > l.segSize {
			if err := l.rotate(int64(len(l.wbuf))); err != nil {
				l.poison(err)
				return err
			}
		}
		if _, err := l.seg.WriteAt(l.wbuf, l.segOff); err != nil {
			l.poison(fmt.Errorf("ticklog write: %w", err))
			return err
		}
		l.segOff += int64(len(l.wbuf))
		l.wbuf = l.wbuf[:0]
	}
	if len(l.unsynced) == 0 {
		return nil
	}
	if err := l.seg.Sync(); err != nil {
		l.poison(fmt.Errorf("ticklog sync: %w", err))
		return err
	}
	l.lastSync = l.now().UnixNano()
	l.mu.Lock()
	for _, p := range l.unsynced {
		d := &l.durable[p.chunk]
		if p.tick > d.Tick {
			d.Tick = p.tick
		}
		if p.lastSeq > d.Seq {
			d.Seq = p.lastSeq
		}
		if p.tick > l.lastLogged[p.chunk] {
			l.lastLogged[p.chunk] = p.tick
		}
	}
	l.mu.Unlock()
	l.unsynced = l.unsynced[:0]
	l.oldestEnq.Store(0)
	return nil
}

// rotate syncs and closes the active segment, then opens the next one
// with a fresh header: per-chunk first ticks continue where the log is,
// under the current epochs. Every byte written to the old segment was
// synced by the flush that preceded the rotation, so closing it loses
// nothing. need extends preallocation when one write group exceeds the
// configured segment size.
func (l *Log) rotate(need int64) error {
	if l.seg != nil {
		if err := l.seg.Close(); err != nil {
			return err
		}
		l.ordinal++
	}
	chunks := make([]codec.SegmentChunk, len(l.cfg.Chunks))
	for i, ch := range l.cfg.Chunks {
		first := l.lastLogged[i] + 1
		chunks[i] = codec.SegmentChunk{Name: ch.Name, Lineage: ch.Lineage, Epoch: l.epochs[i], FirstTick: first}
	}
	hdr := codec.EncodeSegmentHeader(codec.SegmentHeader{
		Version:    codec.SegmentVersion,
		Generation: l.cfg.Generation,
		Ordinal:    l.ordinal,
		Chunks:     chunks,
	})
	size := l.cfg.SegmentBytes
	if min := int64(len(hdr)) + need; min > size {
		size = min
	}
	// Created under a temp name, header synced, then renamed in: a
	// segment name only ever exists with a durable header behind it, so
	// the reader never has to guess whether a bad header is a creation
	// artifact or real corruption.
	path := segmentPath(l.cfg.Dir, l.cfg.Generation, l.ordinal)
	tmp := path + tmpSuffix
	l.fs.Remove(tmp) // a dead activation's leftover, if any
	f, err := l.fs.Create(tmp, size)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(hdr, 0); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := l.fs.Rename(tmp, path); err != nil {
		f.Close()
		return err
	}
	if err := l.fs.SyncDir(l.cfg.Dir); err != nil {
		f.Close()
		return err
	}
	l.seg, l.segOff, l.segSize = f, int64(len(hdr)), size
	l.mu.Lock()
	l.segPath = path
	l.mu.Unlock()
	return nil
}

// ---------- the lag watchdog ----------

// watchdog trips the self-fence when the oldest unsynced record outlives
// MaxDurableLag — a syncer stuck inside a hung sync can't notice on its
// own, and the authority must stop accepting input rather than let the
// real loss window silently grow past the advertised one.
func (l *Log) watchdog() {
	interval := l.cfg.MaxDurableLag / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			if oldest := l.oldestEnq.Load(); oldest != 0 &&
				l.now().UnixNano()-oldest > int64(l.cfg.MaxDurableLag) {
				l.poison(fmt.Errorf("%w: durable lag over %v", ErrStalled, l.cfg.MaxDurableLag))
				return
			}
		}
	}
}
