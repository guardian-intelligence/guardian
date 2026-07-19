package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type authorizationMetrics struct {
	checks   *prometheus.CounterVec
	writes   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newAuthorizationMetrics(registerer prometheus.Registerer) *authorizationMetrics {
	metrics := &authorizationMetrics{
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guardian_authorization_checks_total",
			Help: "Authorization checks partitioned by decision.",
		}, []string{"decision"}),
		writes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guardian_authorization_relationship_writes_total",
			Help: "Authorization relationship write requests partitioned by result.",
		}, []string{"result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "guardian_authorization_request_duration_seconds",
			Help:    "Authorization RPC duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
	}
	registerer.MustRegister(metrics.checks, metrics.writes, metrics.duration)
	return metrics
}

func (m *authorizationMetrics) observeCheck(started time.Time, decision string) {
	if m == nil {
		return
	}
	m.checks.WithLabelValues(decision).Inc()
	m.duration.WithLabelValues("check").Observe(time.Since(started).Seconds())
}

func (m *authorizationMetrics) observeWrite(started time.Time, result string) {
	if m == nil {
		return
	}
	m.writes.WithLabelValues(result).Inc()
	m.duration.WithLabelValues("write_relationships").Observe(time.Since(started).Seconds())
}
