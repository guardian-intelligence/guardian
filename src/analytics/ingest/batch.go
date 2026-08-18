package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// batcher buffers accepted rows and flushes them to ClickHouse on a size or
// age trigger. Fresh time-ordered parts settle ~2.2x larger than merged
// ones, so tiny per-request inserts are the one thing this pipeline must
// never do (docs/analytics-storage-design.md); at guardian's traffic the
// age trigger dominates and produces one part per interval.
//
// Loss semantics are best-effort by design: if ClickHouse stays unreachable
// past the buffer cap the oldest rows drop and the drop is logged — same
// posture as the client beacon's bounded queue.
type batcher struct {
	sink flushSink

	mu     sync.Mutex
	rows   []eventRow
	closed bool

	maxRows   int           // flush trigger
	maxAge    time.Duration // flush trigger
	capRows   int           // drop-oldest bound while sink is down
	lastFlush time.Time

	wake chan struct{}
	done chan struct{}
}

type flushSink interface {
	Insert(ctx context.Context, rows []eventRow) error
}

func newBatcher(sink flushSink, maxRows int, maxAge time.Duration, capRows int) *batcher {
	b := &batcher{
		sink:      sink,
		maxRows:   maxRows,
		maxAge:    maxAge,
		capRows:   capRows,
		lastFlush: time.Now(),
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *batcher) Add(rows []eventRow) {
	b.mu.Lock()
	b.rows = append(b.rows, rows...)
	if over := len(b.rows) - b.capRows; over > 0 {
		slog.Warn("event buffer over cap, dropping oldest", "dropped", over)
		b.rows = append(b.rows[:0:0], b.rows[over:]...)
	}
	full := len(b.rows) >= b.maxRows
	b.mu.Unlock()
	if full {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

func (b *batcher) run() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-b.wake:
		case <-tick.C:
		}
		b.mu.Lock()
		due := len(b.rows) >= b.maxRows || (len(b.rows) > 0 && time.Since(b.lastFlush) >= b.maxAge)
		var take []eventRow
		if due {
			take = b.rows
			b.rows = nil
			b.lastFlush = time.Now()
		}
		b.mu.Unlock()
		if len(take) == 0 {
			continue
		}
		b.flush(take)
	}
}

func (b *batcher) flush(rows []eventRow) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.sink.Insert(ctx, rows); err != nil {
		slog.Error("flush failed, re-buffering", "rows", len(rows), "err", err)
		// Requeue at the front so drop-oldest under a dead sink drops the
		// oldest data first.
		b.mu.Lock()
		b.rows = append(rows, b.rows...)
		if over := len(b.rows) - b.capRows; over > 0 {
			slog.Warn("event buffer over cap, dropping oldest", "dropped", over)
			b.rows = append(b.rows[:0:0], b.rows[over:]...)
		}
		b.mu.Unlock()
		return
	}
	slog.Info("flushed", "rows", len(rows))
}

// Close flushes what remains. Publish handlers must not Add after Close.
func (b *batcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	take := b.rows
	b.rows = nil
	b.mu.Unlock()
	close(b.done)
	if len(take) > 0 {
		b.flush(take)
	}
}
