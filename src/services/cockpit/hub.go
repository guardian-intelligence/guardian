package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"connectrpc.com/connect"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1/cockpitv1connect"
)

const (
	// retention is the hub's in-memory history window.
	retention = 60 * time.Minute
	// ringCapacity is retention at 10 Hz; ~16 B/tick keeps N nodes well
	// under the ~4 MB budget (576 KB per node).
	ringCapacity = int(retention/time.Millisecond) / tickIntervalMs
	// subscriberBuffer is how many flushed frames a subscriber may lag
	// before it is dropped: fan-out must stay O(1) for the hub, so a stalled
	// stream loses its slot rather than backpressuring the flush.
	subscriberBuffer = 8
)

// nodeState is one sampler's accumulated history plus the delta-chain
// cursor. Ticks land in the ring as they arrive; flush encodes the
// not-yet-flushed tail and remembers the last flushed CPU value that the
// next delta frame chains from.
type nodeState struct {
	name      string
	ring      *ring
	unflushed int
	lastCpuBp uint32
	hasLast   bool
}

type subscriber struct {
	ch chan *cockpitv1.Frame
}

// hub merges sampler tick streams, keeps the retention ring, and broadcasts
// 1 s frames. All state is guarded by mu; flush work is O(nodes) +
// O(subscribers × channel-send), independent of subscriber read speed.
type hub struct {
	mu         sync.Mutex
	nodes      []*nodeState
	subs       map[*subscriber]struct{}
	frameCount uint64

	m *hubMetrics
}

func newHub(nodeCount int, m *hubMetrics) *hub {
	h := &hub{subs: map[*subscriber]struct{}{}, m: m}
	for i := 0; i < nodeCount; i++ {
		h.nodes = append(h.nodes, &nodeState{ring: newRing(ringCapacity)})
	}
	return h
}

// ingest records one tick for a node; called by the per-sampler client
// goroutines.
func (h *hub) ingest(node int, name string, t tick) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ns := h.nodes[node]
	if name != "" {
		ns.name = name
	}
	ns.ring.push(t)
	ns.unflushed++
	// Overwrite and eviction only ever remove the oldest ticks, so the
	// unflushed tail can at most shrink to the whole ring.
	ns.ring.evictBefore(t.tsMs - retention.Milliseconds())
	if ns.unflushed > ns.ring.len() {
		ns.unflushed = ns.ring.len()
	}
	h.m.ticksIngested.Add(1)
}

// flush encodes everything unflushed into one frame and broadcasts it. The
// keyframe cadence is global (every keyframeEvery-th flush) so encoding
// happens once regardless of subscriber count; a node whose delta chain
// broke (first appearance, or a backlog overflow) gets a per-series
// keyframe inside a delta frame.
func (h *hub) flush(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	globalKeyframe := h.frameCount%keyframeEvery == 0
	h.frameCount++

	var enc []encodeNode
	baseTs, haveBase := int64(0), false
	for i, ns := range h.nodes {
		if ns.unflushed == 0 {
			continue
		}
		keyframe := globalKeyframe || !ns.hasLast
		if ns.unflushed > maxSeriesTicks {
			// The chain to the last flushed tick is being cut; re-anchor.
			ns.unflushed = maxSeriesTicks
			keyframe = true
		}
		ticks := ns.ring.lastN(ns.unflushed)
		if !haveBase || ticks[0].tsMs < baseTs {
			baseTs, haveBase = ticks[0].tsMs, true
		}
		enc = append(enc, encodeNode{
			index:     uint32(i),
			name:      ns.name,
			keyframe:  keyframe,
			prevCpuBp: ns.lastCpuBp,
			ticks:     ticks,
		})
		ns.lastCpuBp = ticks[len(ticks)-1].cpuBp
		ns.hasLast = true
		ns.unflushed = 0
	}
	if !haveBase {
		// No ticks anywhere (all samplers dark): an empty frame still goes
		// out as stream liveness.
		baseTs = now.UnixMilli() - time.Second.Milliseconds()
	}
	f := encodeFrame(baseTs, enc)
	h.m.framesTotal.Add(1)

	for sub := range h.subs {
		select {
		case sub.ch <- f:
		default:
			// Slow consumer: closing the channel tells its handler goroutine
			// to end the stream; the client reconnects and re-bursts.
			close(sub.ch)
			delete(h.subs, sub)
			h.m.subscribersDropped.Add(1)
		}
	}
	h.m.subscribers.Store(int64(len(h.subs)))
}

// addSubscriber registers a stream and builds its burst frame atomically
// with registration: the burst is a keyframe carrying each node's most
// recent flushed second, and because both happen under one lock the next
// broadcast frame's deltas chain exactly off the burst's last tick.
func (h *hub) addSubscriber() (*subscriber, *cockpitv1.Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var enc []encodeNode
	baseTs, haveBase := int64(0), false
	for i, ns := range h.nodes {
		flushed := ns.ring.len() - ns.unflushed
		n := ticksPerFrame
		if n > flushed {
			n = flushed
		}
		if n == 0 {
			continue
		}
		ticks := make([]tick, n)
		for j := 0; j < n; j++ {
			ticks[j] = ns.ring.at(flushed - n + j)
		}
		if !haveBase || ticks[0].tsMs < baseTs {
			baseTs, haveBase = ticks[0].tsMs, true
		}
		enc = append(enc, encodeNode{
			index:    uint32(i),
			name:     ns.name,
			keyframe: true,
			ticks:    ticks,
		})
	}
	if !haveBase {
		baseTs = time.Now().UnixMilli() - time.Second.Milliseconds()
	}
	sub := &subscriber{ch: make(chan *cockpitv1.Frame, subscriberBuffer)}
	h.subs[sub] = struct{}{}
	h.m.subscribers.Store(int64(len(h.subs)))
	return sub, encodeFrame(baseTs, enc)
}

// removeSubscriber detaches a stream whose handler is returning (client
// hangup). Idempotent with the drop path in flush: whichever side acts
// first wins, and only flush ever closes the channel.
func (h *hub) removeSubscriber(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	h.m.subscribers.Store(int64(len(h.subs)))
}

// run drives the shared flush ticker.
func (h *hub) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.flush(now)
		}
	}
}

// streamSilenceTimeout is how long an established tick stream may go quiet
// before the hub abandons it: a paused or half-dead sampler must read as a
// reconnect, not as a node frozen on screen. Ticks come every 100 ms, so
// 10 s of silence is unambiguous.
const streamSilenceTimeout = 10 * time.Second

// runSamplerClient keeps one sampler's tick stream flowing into the hub,
// reconnecting with a flat backoff — a sampler restart is routine, not an
// error worth escalating past the connected-gauge. Connection-state logging
// is edge-triggered: a dark sampler retries every second for hours, and one
// warn per attempt is log noise, not signal.
func (h *hub) runSamplerClient(ctx context.Context, node int, addr string) {
	client := cockpitv1connect.NewSamplerServiceClient(
		// No client timeout (it would sever the stream); the dial itself is
		// bounded so a black-holed sampler fails over to the retry loop in
		// seconds, not kernel-default minutes.
		&http.Client{Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}},
		samplerBaseURL(addr),
	)
	degraded := false
	for ctx.Err() == nil {
		h.consumeTickStream(ctx, client, node, addr, &degraded)
		sleepCtx(ctx, time.Second)
	}
}

func (h *hub) consumeTickStream(ctx context.Context, client cockpitv1connect.SamplerServiceClient, node int, addr string, degraded *bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.StreamTicks(sctx, connect.NewRequest(&cockpitv1.StreamTicksRequest{}))
	if err != nil {
		if !*degraded && ctx.Err() == nil {
			slog.Warn("sampler unreachable; retrying every second", "node", node, "addr", addr, "err", err)
			*degraded = true
		}
		return
	}
	defer func() { _ = stream.Close() }()

	// Silence watchdog: Receive has no deadline of its own, so a stream
	// whose peer died without a RST would otherwise block forever.
	var lastTick atomic.Int64
	lastTick.Store(time.Now().UnixMilli())
	go func() {
		ticker := time.NewTicker(streamSilenceTimeout / 2)
		defer ticker.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-ticker.C:
				if time.Now().UnixMilli()-lastTick.Load() > streamSilenceTimeout.Milliseconds() {
					slog.Warn("sampler stream silent; abandoning", "node", node, "addr", addr)
					cancel()
					return
				}
			}
		}
	}()

	h.m.samplersConnected.Add(1)
	defer h.m.samplersConnected.Add(-1)
	for stream.Receive() {
		// Connect dials lazily, so a flowing tick — not the call above — is
		// what proves the sampler reachable again.
		if *degraded {
			slog.Info("sampler stream recovered", "node", node, "addr", addr)
			*degraded = false
		}
		lastTick.Store(time.Now().UnixMilli())
		msg := stream.Msg()
		h.ingest(node, msg.GetNode(), tick{
			tsMs:  int64(msg.GetTsMs()),
			cpuBp: msg.GetCpuBp(),
			memBp: msg.GetMemUsedBp(),
		})
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil && !*degraded {
		slog.Warn("sampler stream ended", "node", node, "addr", addr, "err", err)
		*degraded = true
	}
}

func samplerBaseURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// hubService is the Connect front of the hub.
type hubService struct {
	hub *hub
}

func (s *hubService) Subscribe(
	ctx context.Context,
	_ *connect.Request[cockpitv1.SubscribeRequest],
	stream *connect.ServerStream[cockpitv1.Frame],
) error {
	sub, burst := s.hub.addSubscriber()
	defer s.hub.removeSubscriber(sub)
	if err := stream.Send(burst); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case f, ok := <-sub.ch:
			if !ok {
				// Dropped as too slow; ending the stream is the signal.
				return connect.NewError(connect.CodeResourceExhausted, errSlowSubscriber)
			}
			if err := stream.Send(f); err != nil {
				return err
			}
		}
	}
}

var errSlowSubscriber = constError("subscriber fell behind the broadcast")

type constError string

func (e constError) Error() string { return string(e) }
