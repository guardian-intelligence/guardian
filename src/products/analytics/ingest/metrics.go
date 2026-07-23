package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Bounded cardinality: 4 tiers x 2 x 2. The empty-country rate on
// edge_verified traffic is the enrichment canary — header-based enrichment
// fails silently, so deployments/analytics/system/observability.yaml alerts
// on this counter instead of trusting the config.
var eventsIngested = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "analytics_ingest_events_total",
	Help: "Events accepted into the insert batcher.",
}, []string{"trust_tier", "country", "asn"})

// Per-event-name counts exist so product alerts (VMRules) can fire on what
// users actually experienced — e.g. shortty.link_failed — without a
// ClickHouse query path. The name label is confined to this allowlist, not
// the registry: registered PREFIXES admit arbitrary client-chosen suffixes,
// which would otherwise be a cardinality faucet anyone on the internet can
// open.
var meteredEventNames = map[string]struct{}{
	"shortty.link_submitted": {},
	"shortty.link_resolved":  {},
	"shortty.link_failed":    {},
}

var eventsByName = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "analytics_ingest_events_by_name_total",
	Help: "Accepted events by site and event name, allowlisted names only.",
}, []string{"site", "event_name"})

func presenceLabel(present bool) string {
	if present {
		return "present"
	}
	return "empty"
}
