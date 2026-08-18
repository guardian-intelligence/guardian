package park

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var sessionCount atomic.Int64

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mythra_tick_duration_seconds",
		Help:    "Wall time of one authority tick incl. validation, batch append, and fan-out.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mTickLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_tick_lag_seconds",
		Help: "How far the park runs behind its wall-clock tick schedule. Steady state sits inside one tick; sustained growth means the sim cannot keep up; strongly negative means the wall clock stepped backward.",
	}, []string{"park"})
	mClockSkips = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_clock_skips_total", Help: "clock_skip events journaled to repay authority downtime."})
	mRateChanges = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_rate_changes_total", Help: "rate_set events journaled to converge a park to the deployment's tick rate."})
	mAppendDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mythra_journal_append_seconds",
		Help:    "Tick-batched journal append commit time (the Append call alone).",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25, .5},
	})
	mIntentQueueDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mythra_intent_tick_queue_seconds",
		Help:    "Player intent time from server receipt until the authority begins its next tick, by bounded action kind.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25},
	}, []string{"kind"})
	mIntentAuthorityDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mythra_intent_authority_seconds",
		Help:    "Player intent time from server receipt through validation and durable append to fan-out (or rejection), by bounded action kind and result.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25, .5},
	}, []string{"kind", "result"})
	mParks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mythra_parks_open", Help: "Open park authorities."})
	mEventsAppended = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_journal_events_total", Help: "Events appended to the journal."})
	mAppendErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_journal_append_errors_total", Help: "Failed journal appends (authority closes on each)."})
	mSnapshots = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_snapshots_total", Help: "Durable snapshots written."})
	mIntentsRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_intents_rejected_total", Help: "Intents rejected by validation or authorization, by reason."}, []string{"reason"})
	mIntentsDeduped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_intents_deduped_total", Help: "Intents dropped by the (actor, intent_id) idempotency window."})
	mChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_checks_total", Help: "Client hash checks answered."}, []string{"result"})
	mResyncs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_resyncs_total", Help: "Client-requested divergence resyncs."})
	mCatchup = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_catchup_total", Help: "Catch-up material served, by kind."}, []string{"kind"})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_fanout_dropped_total", Help: "Sessions closed for stream backlog."})
	mEpochSwaps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_epoch_swaps_total", Help: "Park module epoch-swap lane outcomes."}, []string{"result"})
)
