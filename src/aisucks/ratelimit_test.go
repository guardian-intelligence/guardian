package main

import (
	"testing"
	"time"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	l := newLimiter()
	for i := 0; i < bucketBurst; i++ {
		if !l.allow("198.51.100.1") {
			t.Fatalf("request %d denied within burst", i)
		}
	}
	if l.allow("198.51.100.1") {
		t.Fatal("request beyond burst allowed")
	}
	// A different address has its own bucket.
	if !l.allow("198.51.100.2") {
		t.Fatal("independent address denied")
	}
	// Refill: back-date the bucket one refill interval and expect a token.
	l.mu.Lock()
	l.buckets["198.51.100.1"].last = time.Now().Add(-bucketRefill)
	l.mu.Unlock()
	if !l.allow("198.51.100.1") {
		t.Fatal("no token after refill interval")
	}
}

func TestLimiterSweepDropsIdleBuckets(t *testing.T) {
	l := newLimiter()
	l.allow("198.51.100.3")
	l.mu.Lock()
	l.buckets["198.51.100.3"].last = time.Now().Add(-2 * sweepEvery)
	l.sweep = time.Now().Add(-2 * sweepEvery)
	l.mu.Unlock()
	l.allow("198.51.100.4") // triggers the sweep
	l.mu.Lock()
	_, kept := l.buckets["198.51.100.3"]
	l.mu.Unlock()
	if kept {
		t.Error("idle bucket survived the sweep; the map must not accumulate addresses")
	}
}
