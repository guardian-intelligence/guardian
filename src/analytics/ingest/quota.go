package main

import (
	"net/netip"
	"sync"
	"time"
)

// Per-visitor-address event quota: a token bucket charged with the batch's
// event count before validation, so junk and genuine events cost the same.
// The ceiling is deliberately generous — an order of magnitude above the
// busiest legitimate client (the beacon flushes ≤4 batches a minute; a
// localStorage replay after a long offline stretch lands a few hundred
// events at once) — because its job is bounding what one address can push
// into ClickHouse, not adjudicating who is a bot. Sub-ceiling junk is met
// reactively: it stays queryable by trust tier and ASN, and the volume
// alerts in deployments/analytics/system/observability.yaml page first.
//
// IPv6 charges the /64 (one household's prefix; per-address would hand a
// SLAAC network 2^64 fresh buckets), IPv4 the address. Only edge-verified
// traffic is charged — anything without a Cloudflare-verified address
// (in-cluster probes, port-forward drives) passes free, and a full table
// admits rather than evicts: an attacker diverse enough to exhaust it is a
// distributed flood, which is the alerts' job, and failing open never
// costs a genuine event.
const (
	quotaEventsPerSec = 20
	quotaBurst        = 4000
	quotaMaxEntries   = 1 << 16
	quotaIdleEviction = 10 * time.Minute
)

type quotaBucket struct {
	tokens float64
	last   time.Time
}

type ipQuota struct {
	mu sync.Mutex
	m  map[netip.Addr]*quotaBucket
}

func newIPQuota() *ipQuota {
	return &ipQuota{m: make(map[netip.Addr]*quotaBucket)}
}

// quotaKey collapses an event-row address (always v6-mapped, see mapToV6)
// to its charging granularity. The zero Addr means "do not charge".
func quotaKey(addr netip.Addr) netip.Addr {
	if !addr.IsValid() || addr == netip.IPv6Unspecified() {
		return netip.Addr{}
	}
	if addr.Is4() || addr.Is4In6() {
		return addr.Unmap()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return netip.Addr{}
	}
	return p.Addr()
}

// allow charges n events against addr's bucket, reporting whether the batch
// fits. A zero key or a table at capacity admits without charging.
func (q *ipQuota) allow(addr netip.Addr, n int, now time.Time) bool {
	key := quotaKey(addr)
	if !key.IsValid() {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	b, ok := q.m[key]
	if !ok {
		if len(q.m) >= quotaMaxEntries {
			q.evictIdle(now)
		}
		if len(q.m) >= quotaMaxEntries {
			return true
		}
		b = &quotaBucket{tokens: quotaBurst, last: now}
		q.m[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * quotaEventsPerSec
	if b.tokens > quotaBurst {
		b.tokens = quotaBurst
	}
	b.last = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

func (q *ipQuota) evictIdle(now time.Time) {
	for k, b := range q.m {
		if now.Sub(b.last) > quotaIdleEviction {
			delete(q.m, k)
		}
	}
}
