package main

// ring is a fixed-capacity tick buffer: pushing onto a full ring overwrites
// the oldest tick, which bounds a node's history to the retention window
// (60 min at 10 Hz) by construction. Not goroutine-safe; the hub serializes
// access under its own lock.
type ring struct {
	buf   []tick
	start int
	n     int
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]tick, capacity)}
}

func (r *ring) len() int { return r.n }

func (r *ring) push(t tick) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = t
		r.n++
		return
	}
	r.buf[r.start] = t
	r.start = (r.start + 1) % len(r.buf)
}

// at returns the i-th tick, oldest first; i must be in [0, len).
func (r *ring) at(i int) tick {
	return r.buf[(r.start+i)%len(r.buf)]
}

// lastN copies out up to k most recent ticks, oldest first.
func (r *ring) lastN(k int) []tick {
	if k > r.n {
		k = r.n
	}
	out := make([]tick, k)
	for i := 0; i < k; i++ {
		out[i] = r.at(r.n - k + i)
	}
	return out
}

// evictBefore drops ticks older than tsMs; wall-clock retention for nodes
// that stopped ticking (a gapped ring would otherwise serve stale history).
func (r *ring) evictBefore(tsMs int64) {
	for r.n > 0 && r.buf[r.start].tsMs < tsMs {
		r.start = (r.start + 1) % len(r.buf)
		r.n--
	}
}
