package trunk

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	mRTT = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chunkies_trunk_rtt_seconds",
		Help:    "Trunk ping round-trip time, observed by the dial side.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25},
	})
	mFrames = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_trunk_frames_total",
		Help: "Trunk frames by wire kind, from this process's perspective.",
	}, []string{"direction", "kind"})
	mBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_trunk_bytes_total",
		Help: "Trunk wire bytes including the 13-byte frame header, from this process's perspective.",
	}, []string{"direction"})
	mAttachments = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chunkies_trunk_attachments",
		Help: "Live dial-side attachments across all trunk connections.",
	})
	mWriteStall = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "chunkies_trunk_write_stall_seconds",
		Help:    "WriteMessage wall time including the shared write-lock wait — how long one frame held, or waited out, the connection's head of line.",
		Buckets: []float64{.00025, .001, .0025, .01, .05, .1, .5, 1, 5, 10},
	})
	mBroadcasts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_trunk_broadcasts_total",
		Help: "Seq-gate outcomes per broadcast frame per attachment; a buffered frame counts again at its final outcome (delivered or deduped).",
	}, []string{"result"})
	mSpliceBufFrames = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chunkies_trunk_splice_buffer_frames",
		Help: "Broadcast frames buffered across attachments while their unicast lanes close catch-up gaps.",
	})
)

// kindName folds a wire kind into a bounded label set; unnamed kinds stay
// observable without minting unbounded series.
func kindName(k byte) string {
	switch k {
	case KindOpen:
		return "open"
	case KindStream:
		return "stream"
	case KindDatagram:
		return "datagram"
	case KindClose:
		return "close"
	case KindPing:
		return "ping"
	case KindPong:
		return "pong"
	case KindBroadcast:
		return "broadcast"
	}
	return "unknown"
}

func countFrame(direction string, kind byte, wireBytes int) {
	mFrames.WithLabelValues(direction, kindName(kind)).Inc()
	mBytes.WithLabelValues(direction).Add(float64(wireBytes))
}

// pingRTT recovers the round trip from a pong's echoed token — the ping's
// send time in UnixNano. A token outside a sane window (peer bug, clock
// step) yields no observation rather than a wild outlier.
func pingRTT(token uint64, now time.Time) (time.Duration, bool) {
	d := now.Sub(time.Unix(0, int64(token)))
	if d < 0 || d > time.Minute {
		return 0, false
	}
	return d, true
}
