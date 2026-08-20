package gateway

import (
	"errors"
	"log"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/guardian-intelligence/guardian/src/chunkies/trunk"
)

var sessionCount atomic.Int64

var (
	mSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "chunkies_sessions", Help: "Connected sessions."}, []string{"role"})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mMints = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_session_mints_total", Help: "POST /session ticket mints."}, []string{"result"})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_datagrams_sent_total", Help: "Datagrams sent."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_datagram_errors_total", Help: "SendDatagram failures."})
	mDgRejected = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_datagrams_rejected_total", Help: "Client datagrams dropped at the gateway (not a well-formed check)."})
	mUnknownFrames = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chunkies_unknown_frames_total", Help: "Client stream frames of unknown kind dropped at the gateway."})
	mTrunkConns = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chunkies_trunk_conns", Help: "Live multiplexed gateway-to-chunk connections."})
	mTrunkConnFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "chunkies_trunk_conn_failures_total", Help: "Gateway-to-chunk connection failures."}, []string{"stage"})
)

func newTrunks(key []byte, dir trunk.Directory) *trunk.Trunks {
	return trunk.NewTrunks(key, dir, trunk.Hooks{
		ConnUp: func(string) { mTrunkConns.Inc() },
		ConnDown: func(addr string, err error) {
			mTrunkConns.Dec()
			// An idle reap is routine lifecycle; counting it as failure
			// would bake noise into anything alerting on this counter.
			if errors.Is(err, trunk.ErrIdle) {
				return
			}
			mTrunkConnFailures.WithLabelValues("run").Inc()
			log.Printf("chunk conn %s down: %v", addr, err)
		},
		DialError: func(addr string) { mTrunkConnFailures.WithLabelValues("dial").Inc() },
	})
}

// gameHandlers wires the admission surface: tickets and mint limits.
type gameHandlers struct {
	tickets     *ticketMint
	maxSessions int
	// directory is the live chunk directory: minting and routing must
	// share one view, so a chunk added at runtime is mintable the same
	// poll it becomes routable.
	directory *chunkDirectory
	game      string
	// defaultChunk answers a mint that names no chunk; empty means the
	// client must always name one.
	defaultChunk string
	anonMints    *anonLimiter
}
