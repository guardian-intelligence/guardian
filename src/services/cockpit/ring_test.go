package main

import "testing"

func mkTick(i int) tick {
	return tick{tsMs: int64(i) * tickIntervalMs, cpuBp: uint32(i), memBp: uint32(i)}
}

func TestRingWraparound(t *testing.T) {
	r := newRing(5)
	for i := 0; i < 12; i++ {
		r.push(mkTick(i))
	}
	if r.len() != 5 {
		t.Fatalf("len = %d, want 5", r.len())
	}
	for i := 0; i < 5; i++ {
		if got, want := r.at(i), mkTick(7+i); got != want {
			t.Fatalf("at(%d) = %+v, want %+v", i, got, want)
		}
	}
}

func TestRingLastN(t *testing.T) {
	r := newRing(10)
	for i := 0; i < 7; i++ {
		r.push(mkTick(i))
	}
	got := r.lastN(3)
	if len(got) != 3 || got[0] != mkTick(4) || got[2] != mkTick(6) {
		t.Fatalf("lastN(3) = %+v", got)
	}
	if got := r.lastN(20); len(got) != 7 || got[0] != mkTick(0) {
		t.Fatalf("lastN over len = %+v", got)
	}
}

func TestRingEvictBefore(t *testing.T) {
	r := newRing(ringCapacity)
	// One hour of ticks, then one more: the first tick is now outside the
	// 60-minute window both by capacity and by wall clock.
	for i := 0; i <= ringCapacity; i++ {
		r.push(mkTick(i))
	}
	if r.len() != ringCapacity {
		t.Fatalf("len = %d, want %d", r.len(), ringCapacity)
	}
	newest := r.at(r.len() - 1)
	r.evictBefore(newest.tsMs - retention.Milliseconds())
	if got := r.at(0); got.tsMs < newest.tsMs-retention.Milliseconds() {
		t.Fatalf("oldest tick %d predates the retention window", got.tsMs)
	}
	// Evicting everything drains the ring, and it keeps working after.
	r.evictBefore(newest.tsMs + 1)
	if r.len() != 0 {
		t.Fatalf("len after full eviction = %d", r.len())
	}
	r.push(mkTick(99))
	if r.len() != 1 || r.at(0) != mkTick(99) {
		t.Fatalf("ring unusable after full eviction: %+v", r.lastN(1))
	}
}
