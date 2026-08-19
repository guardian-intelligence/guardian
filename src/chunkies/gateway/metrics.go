package gateway

import (
	"errors"
	"log"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/guardian-intelligence/guardian/src/chunkies/parkproxy"
)

var sessionCount atomic.Int64

var (
	mSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_sessions", Help: "Connected sessions."}, []string{"role"})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mMints = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_session_mints_total", Help: "POST /session ticket mints."}, []string{"result"})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagrams_sent_total", Help: "Datagrams sent."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagram_errors_total", Help: "SendDatagram failures."})
	mDgRejected = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagrams_rejected_total", Help: "Client datagrams dropped at the gateway (not a well-formed check)."})
	mUnknownFrames = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_unknown_frames_total", Help: "Client stream frames of unknown kind dropped at the gateway."})
	mParkConns = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mythra_park_conns", Help: "Live multiplexed gateway-to-park connections."})
	mParkConnFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_park_conn_failures_total", Help: "Gateway-to-park connection failures."}, []string{"stage"})
)

func newParkPool(key []byte) *parkproxy.Pool {
	return parkproxy.NewPool(key, parkproxy.Hooks{
		ConnUp: func(string) { mParkConns.Inc() },
		ConnDown: func(addr string, err error) {
			mParkConns.Dec()
			// An idle reap is routine lifecycle; counting it as failure
			// would bake noise into anything alerting on this counter.
			if errors.Is(err, parkproxy.ErrIdle) {
				return
			}
			mParkConnFailures.WithLabelValues("run").Inc()
			log.Printf("park conn %s down: %v", addr, err)
		},
		DialError: func(addr string) { mParkConnFailures.WithLabelValues("dial").Inc() },
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
	anonMints *anonLimiter
}
