package trunk

// Coverage for the metrics whose correctness is invisible in transport
// behavior: the RTT token round trip, the frame/byte ledgers, and the
// gauges that must settle when connections die out from under their
// attachments.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPingRTTTokenRoundTrip(t *testing.T) {
	sent := time.Now()
	// The token is exactly what pingLoop stamps into the ping's SID.
	token := uint64(sent.UnixNano())
	d, ok := pingRTT(token, sent.Add(3*time.Millisecond))
	if !ok || d != 3*time.Millisecond {
		t.Fatalf("rtt = %v ok=%v, want 3ms", d, ok)
	}
	if _, ok := pingRTT(token, sent.Add(-time.Millisecond)); ok {
		t.Fatal("a pong from the future observed an RTT")
	}
	if _, ok := pingRTT(0, sent); ok {
		t.Fatal("a garbage token observed as a decades-long RTT")
	}
}

func rttObservations(t *testing.T) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "chunkies_trunk_rtt_seconds" {
			return mf.GetMetric()[0].GetHistogram().GetSampleCount()
		}
	}
	return 0
}

func TestDialSideObservesRTTFromPong(t *testing.T) {
	before := rttObservations(t)
	chunk := newFakeChunkieServer(t, nil)
	down := make(chan struct{})
	pool := NewPool(testKey, Hooks{ConnDown: func(string, error) { close(down) }})
	pool.PingEvery = 10 * time.Millisecond
	pool.IdleAfter = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := pool.Open(ctx, chunk.addr(), Open{Game: "toy", Sub: "alice", Chunk: "chunk-test", Role: "player"})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return rttObservations(t) > before }) {
		t.Fatal("no RTT observed from the ping/pong exchange")
	}
	// Wait out the idle reap so this test's fast pinger doesn't keep
	// mutating the ledgers under later tests.
	a.Close("")
	select {
	case <-down:
	case <-time.After(5 * time.Second):
		t.Fatal("conn never reaped")
	}
}

var wireKindNames = []string{"open", "stream", "datagram", "close", "ping", "pong", "broadcast", "unknown"}

type frameLedger struct {
	frames map[string]map[string]float64
	bytes  map[string]float64
}

func readLedgerOnce() frameLedger {
	l := frameLedger{frames: map[string]map[string]float64{}, bytes: map[string]float64{}}
	for _, dir := range []string{"send", "recv"} {
		l.frames[dir] = map[string]float64{}
		for _, k := range wireKindNames {
			l.frames[dir][k] = testutil.ToFloat64(mFrames.WithLabelValues(dir, k))
		}
		l.bytes[dir] = testutil.ToFloat64(mBytes.WithLabelValues(dir))
	}
	return l
}

func ledgersEqual(a, b frameLedger) bool {
	for _, dir := range []string{"send", "recv"} {
		if a.bytes[dir] != b.bytes[dir] {
			return false
		}
		for _, k := range wireKindNames {
			if a.frames[dir][k] != b.frames[dir][k] {
				return false
			}
		}
	}
	return true
}

// readLedger re-reads until two passes agree: counter reads are not one
// atomic snapshot, and a frame landing mid-read would skew the deltas.
func readLedger() frameLedger {
	l := readLedgerOnce()
	for {
		next := readLedgerOnce()
		if ledgersEqual(l, next) {
			return l
		}
		l = next
	}
}

// One known exchange over a pipe checks the ledgers. Connections lingering
// from other tests can add pings and pongs — zero-payload frames — so the
// byte expectation derives from the observed frame counts rather than
// assuming a quiet registry.
func TestFrameAndByteLedgers(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() { left.Close(); right.Close() })
	cl, cr := &Conn{c: left}, &Conn{c: right}
	go func() {
		for {
			if _, err := cr.ReadMessage(); err != nil {
				return
			}
		}
	}()
	pong := make(chan Msg, 1)
	go func() {
		for {
			m, err := cl.ReadMessage()
			if err != nil {
				return
			}
			if m.Kind == KindPong {
				pong <- m
			}
		}
	}()

	before := readLedger()
	if err := cl.WriteStream(1, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := cl.WriteDatagram(1, make([]byte, 7)); err != nil {
		t.Fatal(err)
	}
	if err := cl.WriteMessage(KindBroadcast, 0, make([]byte, 25)); err != nil {
		t.Fatal(err)
	}
	if err := cl.WriteMessage(KindPing, 42, nil); err != nil {
		t.Fatal(err)
	}
	// The pong's arrival proves the pipe's serial reader consumed all four
	// frames, so every count below is final.
	select {
	case m := <-pong:
		if m.SID != 42 {
			t.Fatalf("pong token = %d, want the ping's 42 — RTT rides this echo", m.SID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no pong answered the ping")
	}
	after := readLedger()

	const payloadBytes = 100 + 7 + 25
	for _, dir := range []string{"send", "recv"} {
		for kind, want := range map[string]float64{"stream": 1, "datagram": 1, "broadcast": 1} {
			if d := after.frames[dir][kind] - before.frames[dir][kind]; d != want {
				t.Errorf("%s %s frames delta = %v, want %v", dir, kind, d, want)
			}
		}
		for _, kind := range []string{"ping", "pong"} {
			if d := after.frames[dir][kind] - before.frames[dir][kind]; d < 1 {
				t.Errorf("%s %s frames delta = %v, want at least 1", dir, kind, d)
			}
		}
		var frames float64
		for _, k := range wireKindNames {
			frames += after.frames[dir][k] - before.frames[dir][k]
		}
		if d := after.bytes[dir] - before.bytes[dir]; d != 13*frames+payloadBytes {
			t.Errorf("%s bytes delta = %v across %v frames, want %v", dir, d, frames, 13*frames+payloadBytes)
		}
	}
}

func broadcastCounts() map[string]float64 {
	out := map[string]float64{}
	for _, r := range []string{"delivered", "deduped", "buffered", "overflow"} {
		out[r] = testutil.ToFloat64(mBroadcasts.WithLabelValues(r))
	}
	return out
}

// The attachment gauge and the splice-buffer gauge must both settle when
// a connection dies with live attachments still holding buffered frames.
func TestGaugesSettleOnConnLoss(t *testing.T) {
	attachBefore := testutil.ToFloat64(mAttachments)
	bufBefore := testutil.ToFloat64(mSpliceBufFrames)
	chunk := welcomeServer(t)
	pool := NewPool(testKey, Hooks{})
	a := openAt(t, pool, chunk.addr(), "alice", "chunk-test", 5)
	b := openAt(t, pool, chunk.addr(), "bob", "chunk-test", 5)
	if d := testutil.ToFloat64(mAttachments) - attachBefore; d != 2 {
		t.Fatalf("attachments delta = %v, want 2", d)
	}
	// Seq 6 never arrives, so 7 gaps past pos 5 and buffers on both.
	sendBroadcast(t, chunk.conn(0), "toy", "chunk-test", tickFrameFor(7))
	if !waitFor(t, 5*time.Second, func() bool { return testutil.ToFloat64(mSpliceBufFrames)-bufBefore == 2 }) {
		t.Fatalf("splice buffer delta = %v, want 2", testutil.ToFloat64(mSpliceBufFrames)-bufBefore)
	}
	chunk.conn(0).Close()
	for _, att := range []*Attachment{a, b} {
		select {
		case <-att.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("attachment outlived its connection")
		}
	}
	if d := testutil.ToFloat64(mAttachments) - attachBefore; d != 0 {
		t.Fatalf("attachments delta after conn loss = %v, want 0", d)
	}
	if d := testutil.ToFloat64(mSpliceBufFrames) - bufBefore; d != 0 {
		t.Fatalf("splice buffer delta after conn loss = %v, want 0", d)
	}
}
