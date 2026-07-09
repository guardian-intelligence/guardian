package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1/cockpitv1connect"
)

func feed(h *hub, node int, name string, ticks []tick) {
	for _, tk := range ticks {
		h.ingest(node, name, tk)
	}
}

// TestBurstOnSubscribe: a new subscriber's first frame is a keyframe
// carrying the most recent flushed second, and the next broadcast frame is
// steady-state (delta) yet decodes into an exact continuation.
func TestBurstOnSubscribe(t *testing.T) {
	h := newHub(1, &hubMetrics{})
	base := int64(1_700_000_000_000)
	series := synthSeries(3, base, 40)
	for i := 0; i < 30; i += ticksPerFrame {
		feed(h, 0, "node-a", series[i:i+ticksPerFrame])
		h.flush(time.UnixMilli(series[i+ticksPerFrame-1].tsMs))
	}

	sub, burst := h.addSubscriber()
	defer h.removeSubscriber(sub)

	if len(burst.GetNodes()) != 1 {
		t.Fatalf("burst has %d series, want 1", len(burst.GetNodes()))
	}
	bs := burst.GetNodes()[0]
	if bs.CpuKeyframeBp == nil || bs.GetName() != "node-a" {
		t.Fatalf("burst is not a named keyframe: %+v", bs)
	}
	if got, want := int64(burst.GetBaseTsMs()), series[20].tsMs; got != want {
		t.Fatalf("burst baseTs = %d, want %d (start of last flushed second)", got, want)
	}

	dec := newFrameDecoder()
	decoded, err := dec.decode(burst)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded[0].cpuBp) != ticksPerFrame {
		t.Fatalf("burst carries %d ticks, want %d", len(decoded[0].cpuBp), ticksPerFrame)
	}
	for i, cpu := range decoded[0].cpuBp {
		if want := series[20+i].cpuBp; cpu != want {
			t.Fatalf("burst tick %d: cpu = %d, want %d", i, cpu, want)
		}
	}

	// The next flush chains off the burst exactly, as a delta series.
	feed(h, 0, "node-a", series[30:40])
	h.flush(time.UnixMilli(series[39].tsMs))
	var next *cockpitv1.Frame
	select {
	case next = <-sub.ch:
	default:
		t.Fatal("no frame broadcast after flush")
	}
	ns := next.GetNodes()[0]
	if ns.CpuKeyframeBp != nil || ns.GetName() != "" {
		t.Fatalf("steady-state frame is not a delta series: %+v", ns)
	}
	decoded, err = dec.decode(next)
	if err != nil {
		t.Fatal(err)
	}
	for i, cpu := range decoded[0].cpuBp {
		if want := series[30+i].cpuBp; cpu != want {
			t.Fatalf("delta tick %d: cpu = %d, want %d", i, cpu, want)
		}
	}
}

// TestKeyframeCadence: flushes 0 and keyframeEvery re-anchor; everything
// between is delta-coded.
func TestKeyframeCadence(t *testing.T) {
	h := newHub(1, &hubMetrics{})
	sub, _ := h.addSubscriber()
	defer h.removeSubscriber(sub)
	base := int64(1_700_000_000_000)
	series := synthSeries(4, base, (keyframeEvery+1)*ticksPerFrame)
	for i := 0; i <= keyframeEvery; i++ {
		feed(h, 0, "node-a", series[i*ticksPerFrame:(i+1)*ticksPerFrame])
		h.flush(time.UnixMilli(series[(i+1)*ticksPerFrame-1].tsMs))
		f := <-sub.ch
		isKeyframe := f.GetNodes()[0].CpuKeyframeBp != nil
		if want := i == 0 || i == keyframeEvery; isKeyframe != want {
			t.Fatalf("flush %d: keyframe = %v, want %v", i, isKeyframe, want)
		}
	}
}

// TestFanOutIdenticalAndStalledDropped: every healthy subscriber receives
// the identical frame; a subscriber that stops reading is dropped (stream
// channel closed) without the others missing a frame.
func TestFanOutIdenticalAndStalledDropped(t *testing.T) {
	m := &hubMetrics{}
	h := newHub(1, m)
	stalled, _ := h.addSubscriber()
	healthy1, _ := h.addSubscriber()
	healthy2, _ := h.addSubscriber()

	base := int64(1_700_000_000_000)
	series := synthSeries(5, base, (subscriberBuffer+2)*ticksPerFrame)
	for i := 0; i < subscriberBuffer+2; i++ {
		feed(h, 0, "node-a", series[i*ticksPerFrame:(i+1)*ticksPerFrame])
		h.flush(time.UnixMilli(series[(i+1)*ticksPerFrame-1].tsMs))
		f1 := <-healthy1.ch
		f2 := <-healthy2.ch
		if f1 != f2 {
			t.Fatalf("flush %d: subscribers got different frames", i)
		}
	}

	// The stalled subscriber absorbed subscriberBuffer frames, then was
	// dropped: its channel drains and then reports closed.
	for i := 0; i < subscriberBuffer; i++ {
		if _, ok := <-stalled.ch; !ok {
			t.Fatalf("stalled channel closed after %d frames, want %d buffered", i, subscriberBuffer)
		}
	}
	if _, ok := <-stalled.ch; ok {
		t.Fatal("stalled subscriber channel still open; expected drop")
	}
	if got := m.subscribersDropped.Load(); got != 1 {
		t.Fatalf("subscribersDropped = %d, want 1", got)
	}
	if got := m.subscribers.Load(); got != 2 {
		t.Fatalf("subscribers gauge = %d, want 2", got)
	}
}

// TestSubscribeConnectE2E drives the real Connect stack: burst on connect,
// then a broadcast frame, then clean detach on client cancel.
func TestSubscribeConnectE2E(t *testing.T) {
	m := &hubMetrics{}
	h := newHub(1, m)
	base := int64(1_700_000_000_000)
	series := synthSeries(6, base, 20)
	feed(h, 0, "node-a", series[:10])
	h.flush(time.UnixMilli(series[9].tsMs))

	mux := http.NewServeMux()
	path, handler := cockpitv1connect.NewCockpitStreamServiceHandler(&hubService{hub: h})
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := cockpitv1connect.NewCockpitStreamServiceClient(srv.Client(), srv.URL)
	stream, err := client.Subscribe(ctx, connect.NewRequest(&cockpitv1.SubscribeRequest{}))
	if err != nil {
		t.Fatal(err)
	}

	if !stream.Receive() {
		t.Fatalf("no burst frame: %v", stream.Err())
	}
	burst := stream.Msg()
	if len(burst.GetNodes()) != 1 || burst.GetNodes()[0].CpuKeyframeBp == nil {
		t.Fatalf("first frame is not a keyframe burst: %+v", burst)
	}

	// The handler registers the subscriber before sending the burst, so a
	// flush after the burst arrives is guaranteed to reach this stream.
	feed(h, 0, "node-a", series[10:])
	h.flush(time.UnixMilli(series[19].tsMs))
	if !stream.Receive() {
		t.Fatalf("no broadcast frame: %v", stream.Err())
	}
	if f := stream.Msg(); f.GetNodes()[0].CpuKeyframeBp != nil {
		t.Fatalf("second frame should be steady-state, got keyframe: %+v", f)
	}

	// Client hangup detaches the subscriber (no goroutine or slot leak).
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for m.subscribers.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("subscriber not detached after cancel; gauge = %d", m.subscribers.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
