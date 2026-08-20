package checkpoint

import (
	"bytes"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/chunkie/ticklog"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// Config bounds the cadence engine. The defaults are the design of
// record's: ~17s ±20% per-chunk jitter (an interleaved chunkie never
// snapshots all chunks at once), keep-3 ring.
type Config struct {
	Cadence time.Duration // default 17s
	Jitter  float64       // default 0.2 of Cadence
	Keep    int           // ring size; default 3, floor 2
}

func (c Config) withDefaults() Config {
	if c.Cadence <= 0 {
		c.Cadence = 17 * time.Second
	}
	if c.Jitter <= 0 {
		c.Jitter = 0.2
	}
	if c.Keep < 2 {
		c.Keep = 3
	}
	return c
}

// ProveFunc restores raw (inflated) state into a fresh sim instance
// and demands the world hash — supplied by the chunkie, which owns the
// module runtime; the store stays module-blind.
type ProveFunc func(state []byte, wh uint64) error

// Snapshotter is the per-activation cadence engine. The tick loop pays
// only for the sim_snapshot call and hands the bytes to Submit;
// deflate, the smeared write, retention, and WAL trimming all run on
// one background lane. One manifest in flight per chunk — a lane still
// busy at the next cadence skips (and counts), never queues.
type Snapshotter struct {
	st  Store
	wal *ticklog.Log // may be nil; Retain then trims nothing
	cfg Config

	mu   sync.Mutex
	next map[string]time.Time
	busy map[string]bool

	lane    chan work
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
	now     func() time.Time // test seam; nil = time.Now
}

type work struct{ m codec.Checkpoint }

func New(st Store, wal *ticklog.Log, cfg Config) *Snapshotter {
	s := &Snapshotter{
		st: st, wal: wal, cfg: cfg.withDefaults(),
		next: map[string]time.Time{}, busy: map[string]bool{},
		lane: make(chan work, 8), done: make(chan struct{}), stopped: make(chan struct{}),
		now: time.Now,
	}
	go s.run()
	return s
}

// Due reports whether chunk owes a checkpoint. The first call arms the
// chunk's jittered schedule without being due — a fleet restart must
// not checkpoint every chunk at boot.
func (s *Snapshotter) Due(chunk string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.next[chunk]
	if !ok {
		s.next[chunk] = now.Add(s.jittered())
		return false
	}
	if now.Before(at) || s.busy[chunk] {
		return false
	}
	return true
}

// Submit hands one manifest to the background lane. m.State carries
// the RAW sim snapshot; the lane deflates it. Submit returns
// immediately — the caller's tick-barrier cost is the sim_snapshot
// call it already made.
func (s *Snapshotter) Submit(m codec.Checkpoint) {
	select {
	case <-s.done:
		mCkptSkipped.WithLabelValues("closed").Inc()
		return
	default:
	}
	s.mu.Lock()
	if s.busy[m.Chunk] {
		s.mu.Unlock()
		mCkptSkipped.WithLabelValues("busy").Inc()
		return
	}
	s.busy[m.Chunk] = true
	s.next[m.Chunk] = s.now().Add(s.jittered())
	s.mu.Unlock()
	select {
	case s.lane <- work{m: m}:
	default:
		s.mu.Lock()
		s.busy[m.Chunk] = false
		s.mu.Unlock()
		mCkptSkipped.WithLabelValues("lane_full").Inc()
	}
}

func (s *Snapshotter) jittered() time.Duration {
	j := 1 + s.cfg.Jitter*(2*rand.Float64()-1)
	return time.Duration(float64(s.cfg.Cadence) * j)
}

// Force writes one manifest and returns only after it is durable AND
// proven — the promotion barrier's step 2 (a checkpoint under the new
// pair, restore-verified, before the old epoch may die) and the
// discontinuity sites' floor. Proving reads back from disk: the claim
// is about what recovery will find, not what the caller handed in.
func (s *Snapshotter) Force(ctx context.Context, m codec.Checkpoint, prove ProveFunc) (Ref, error) {
	deflated, err := deflate(m.State)
	if err != nil {
		return Ref{}, err
	}
	raw := m.State
	m.State = deflated
	ref, err := s.st.Put(ctx, m)
	if err != nil {
		return Ref{}, err
	}
	// A checkpoint that fails its proof must not stay on disk: it would
	// be the chunk's newest CRC-valid ref, exactly what naive recovery
	// prefers.
	back, err := s.st.Load(ref)
	if err != nil {
		s.st.Remove(ref)
		return Ref{}, err
	}
	state, err := Inflate(back.State)
	if err != nil {
		s.st.Remove(ref)
		return Ref{}, err
	}
	if !bytes.Equal(state, raw) {
		s.st.Remove(ref)
		return Ref{}, fmt.Errorf("checkpoint %s: %w: state bytes changed across the disk round-trip", ref.name(), ErrMismatch)
	}
	if prove != nil {
		if err := prove(state, back.WH); err != nil {
			s.st.Remove(ref)
			return Ref{}, fmt.Errorf("checkpoint %s refused proof: %w", ref.name(), err)
		}
	}
	if err := s.st.MarkProven(ref); err != nil {
		s.st.Remove(ref)
		return Ref{}, err
	}
	ref.Proven = true
	mCkptWrites.WithLabelValues("forced").Inc()
	mCkptLastUnix.WithLabelValues(m.Chunk).Set(float64(time.Now().Unix()))
	return ref, nil
}

func (s *Snapshotter) run() {
	defer close(s.stopped)
	for {
		select {
		case w := <-s.lane:
			s.write(w.m)
		case <-s.done:
			// Drain what Submit already accepted, then stop; Close
			// sweeps any straggler that races this drain.
			for {
				select {
				case w := <-s.lane:
					s.write(w.m)
				default:
					return
				}
			}
		}
	}
}

func (s *Snapshotter) write(m codec.Checkpoint) {
	defer func() {
		s.mu.Lock()
		s.busy[m.Chunk] = false
		s.mu.Unlock()
	}()
	start := time.Now()
	deflated, err := deflate(m.State)
	if err == nil {
		m.State = deflated
		// No deadline: the store's smear budget legitimately stretches a
		// large blob past any fixed timeout, and aborting would retry the
		// same abort every cadence forever. A wedged disk is observable —
		// stale chunkies_ckpt_last_unix, busy skips — and latches the WAL
		// watchdog on the same volume anyway.
		_, err = s.st.Put(context.Background(), m)
	}
	if err != nil {
		mCkptWrites.WithLabelValues("error").Inc()
		log.Printf("chunk %s: checkpoint write failed: %v", m.Chunk, err)
		return
	}
	mCkptWrites.WithLabelValues("ok").Inc()
	mCkptWriteDur.Observe(time.Since(start).Seconds())
	mCkptBytes.Set(float64(len(m.State)))
	mCkptLastUnix.WithLabelValues(m.Chunk).Set(float64(time.Now().Unix()))
	if err := Retain(s.st, s.wal, s.cfg.Keep); err != nil {
		log.Printf("chunk %s: retention: %v", m.Chunk, err)
	}
}

// Close stops the lane and returns once accepted work has landed (or
// ctx expires — the write is then abandoned to the dying process).
// After the run loop exits, Close sweeps any straggler a racing Submit
// buffered; Submits strictly after Close are dropped and counted.
func (s *Snapshotter) Close(ctx context.Context) error {
	s.once.Do(func() { close(s.done) })
	select {
	case <-s.stopped:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		select {
		case w := <-s.lane:
			s.write(w.m)
		default:
			return nil
		}
	}
}

func deflate(b []byte) ([]byte, error) {
	var z bytes.Buffer
	// Default, not Best: the dense-grid game is the sizing case, and a
	// max-effort deflate every cadence is CPU the tick loop shares.
	w, err := flate.NewWriter(&z, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return z.Bytes(), nil
}

// Inflate decompresses a manifest's State field back to the raw bytes
// sim_restore expects — the recovery ladder's half of Submit's deflate.
func Inflate(b []byte) ([]byte, error) {
	// Strict: a truncated stream must fail loudly. The manifest CRC
	// cannot catch truncation that happened before encoding, and an
	// amputated world restoring silently is the worst outcome here.
	r := flate.NewReader(bytes.NewReader(b))
	defer r.Close()
	return io.ReadAll(r)
}
