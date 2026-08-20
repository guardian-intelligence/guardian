package checkpoint

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	mCkptWrites = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_ckpt_writes_total",
		Help: "Checkpoint writes by result (ok, forced, error)."}, []string{"result"})
	mCkptSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_ckpt_skipped_total",
		Help: "Checkpoint cadences skipped (busy: prior write still in flight; lane_full)."}, []string{"reason"})
	mCkptWriteDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chunkies_ckpt_write_seconds",
		Help:    "Background checkpoint lane time: deflate through durable rename.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	})
	mCkptBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chunkies_ckpt_bytes",
		Help: "Size of the newest checkpoint's deflated state blob."})
	mCkptLastUnix = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chunkies_ckpt_last_unix",
		Help: "Unix time of the chunk's newest durable checkpoint; age = now() - this, alert on the cadence's multiple."}, []string{"chunk"})
)
