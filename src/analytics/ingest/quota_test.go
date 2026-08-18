package main

import (
	"net/netip"
	"testing"
	"time"
)

func TestQuotaKeyGranularity(t *testing.T) {
	v4 := mapToV6(netip.MustParseAddr("203.0.113.7"))
	if got := quotaKey(v4); got != netip.MustParseAddr("203.0.113.7") {
		t.Fatalf("v4 key = %v", got)
	}
	a := quotaKey(netip.MustParseAddr("2001:db8::1"))
	b := quotaKey(netip.MustParseAddr("2001:db8::ffff:9"))
	if a != b {
		t.Fatalf("same /64 must share a key: %v vs %v", a, b)
	}
	c := quotaKey(netip.MustParseAddr("2001:db8:0:1::1"))
	if a == c {
		t.Fatal("different /64s must not share a key")
	}
	if quotaKey(netip.IPv6Unspecified()).IsValid() {
		t.Fatal("unspecified address must not be charged")
	}
	if quotaKey(netip.Addr{}).IsValid() {
		t.Fatal("zero address must not be charged")
	}
}

func TestQuotaBurstThenRefill(t *testing.T) {
	q := newIPQuota()
	ip := mapToV6(netip.MustParseAddr("203.0.113.7"))
	now := time.UnixMilli(1_800_000_000_000)

	if !q.allow(ip, quotaBurst, now) {
		t.Fatal("a full burst must fit an untouched bucket")
	}
	if q.allow(ip, 1, now) {
		t.Fatal("bucket must be empty after the burst")
	}
	if !q.allow(ip, quotaEventsPerSec, now.Add(time.Second)) {
		t.Fatal("one second must refill one second's worth")
	}
	if !q.allow(mapToV6(netip.MustParseAddr("203.0.113.8")), 1, now) {
		t.Fatal("another address must have its own bucket")
	}
}

func TestQuotaFailsOpenWithoutVerifiedIP(t *testing.T) {
	q := newIPQuota()
	now := time.UnixMilli(1_800_000_000_000)
	for i := 0; i < 10; i++ {
		if !q.allow(netip.IPv6Unspecified(), quotaBurst, now) {
			t.Fatal("unverified traffic must never be charged")
		}
	}
}

func TestQuotaTableCapacityFailsOpen(t *testing.T) {
	q := newIPQuota()
	now := time.UnixMilli(1_800_000_000_000)
	q.m[quotaKey(mapToV6(netip.MustParseAddr("198.51.100.1")))] = &quotaBucket{tokens: 0, last: now}
	for len(q.m) < quotaMaxEntries {
		var a [16]byte
		a[0] = 0x20
		a[1] = 0x01
		a[2] = byte(len(q.m) >> 16)
		a[3] = byte(len(q.m) >> 8)
		a[4] = byte(len(q.m))
		q.m[netip.AddrFrom16(a)] = &quotaBucket{tokens: 0, last: now}
	}
	fresh := mapToV6(netip.MustParseAddr("203.0.113.99"))
	if !q.allow(fresh, quotaBurst, now) {
		t.Fatal("a full table must admit new addresses, not block them")
	}
	// Idle entries age out and charging resumes for newcomers.
	later := now.Add(quotaIdleEviction + time.Minute)
	if !q.allow(fresh, quotaBurst, later) {
		t.Fatal("post-eviction newcomer must get a fresh bucket")
	}
	if q.allow(fresh, quotaBurst, later) {
		t.Fatal("post-eviction newcomer must be charged normally")
	}
}
