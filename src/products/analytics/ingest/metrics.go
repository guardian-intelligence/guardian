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

func presenceLabel(present bool) string {
	if present {
		return "present"
	}
	return "empty"
}
