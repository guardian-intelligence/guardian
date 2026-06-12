package main

import (
	"sync"
	"time"
)

// limiter is a per-IP token bucket that exists only in process memory —
// the privacy promise allows abuse signals that die with the process and
// nothing more durable. Buckets idle past the horizon are dropped, so the
// map cannot grow into a de facto visitor registry.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	sweep   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	bucketBurst  = 5                // submissions allowed at once
	bucketRefill = time.Minute / 6  // one token every 10s
	sweepEvery   = 10 * time.Minute // also the idle horizon for dropping buckets
)

func newLimiter() *limiter {
	return &limiter{buckets: make(map[string]*bucket), sweep: time.Now()}
}

func (l *limiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.sweep) > sweepEvery {
		for k, b := range l.buckets {
			if now.Sub(b.last) > sweepEvery {
				delete(l.buckets, k)
			}
		}
		l.sweep = now
	}

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: bucketBurst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() / bucketRefill.Seconds()
	if b.tokens > bucketBurst {
		b.tokens = bucketBurst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
