package chunkie

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var sessionCount atomic.Int64

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chunkies_tick_duration_seconds",
		Help:    "Wall time of one authority tick incl. validation, batch append, and fan-out.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mTickLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chunkies_tick_lag_seconds",
		Help: "How far the chunk runs behind its wall-clock tick schedule. Steady state sits inside one tick; sustained growth means the sim cannot keep up; strongly negative means the wall clock stepped backward.",
	}, []string{"chunk"})
	mClockSkips = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_clock_skips_total", Help: "clock_skip events journaled to repay authority downtime."})
	mRateChanges = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_rate_changes_total", Help: "rate_set events journaled to converge a chunk to the deployment's tick rate."})
	mAppendDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chunkies_journal_append_seconds",
		Help:    "Tick-batched journal append commit time (the Append call alone).",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25, .5},
	})
	mIntentQueueDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chunkies_intent_tick_queue_seconds",
		Help:    "Player intent time from server receipt until the authority begins its next tick, by bounded action kind.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25},
	}, []string{"kind"})
	mIntentAuthorityDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chunkies_intent_authority_seconds",
		Help:    "Player intent time from server receipt through validation and durable append to fan-out (or rejection), by bounded action kind and result.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25, .5},
	}, []string{"kind", "result"})
	mChunks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chunkies_chunks_open", Help: "Open chunk authorities."})
	mEventsAppended = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_journal_events_total", Help: "Events appended to the journal."})
	mAppendErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_journal_append_errors_total", Help: "Failed journal appends (authority closes on each)."})
	mSnapshots = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_snapshots_total", Help: "Durable snapshots written."})
	mIntentsRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_intents_rejected_total", Help: "Intents rejected by validation or authorization, by reason."}, []string{"reason"})
	mIntentsDeduped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_intents_deduped_total", Help: "Intents dropped by the (actor, intent_id) idempotency window."})
	mChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_checks_total", Help: "Client hash checks answered."}, []string{"result"})
	mResyncs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_resyncs_total", Help: "Client-requested divergence resyncs."})
	mCatchup = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_catchup_total", Help: "Catch-up material served, by kind."}, []string{"kind"})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_fanout_dropped_total", Help: "Sessions closed for stream backlog."})
	mInboundDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_inbound_dropped_total", Help: "Uplink frames shed because a session's intent drain was stalled."})
	mEpochSwaps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_epoch_swaps_total", Help: "Chunk module epoch-swap lane outcomes."}, []string{"result"})
)
