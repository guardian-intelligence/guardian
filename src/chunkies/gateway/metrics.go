package gateway

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
)

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
