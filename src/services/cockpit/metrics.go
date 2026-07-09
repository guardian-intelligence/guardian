package main

import (
	"fmt"
	"io"
	"sync/atomic"
)

type hubMetrics struct {
	ticksIngested      atomic.Uint64
	framesTotal        atomic.Uint64
	subscribers        atomic.Int64
	subscribersDropped atomic.Uint64
	samplersConnected  atomic.Int64
}

func (m *hubMetrics) render(w io.Writer) {
	fmt.Fprintf(w, "# HELP cockpit_ticks_ingested_total Sampler ticks merged into the ring.\n")
	fmt.Fprintf(w, "# TYPE cockpit_ticks_ingested_total counter\n")
	fmt.Fprintf(w, "cockpit_ticks_ingested_total %d\n", m.ticksIngested.Load())
	fmt.Fprintf(w, "# HELP cockpit_frames_total Frames flushed to subscribers.\n")
	fmt.Fprintf(w, "# TYPE cockpit_frames_total counter\n")
	fmt.Fprintf(w, "cockpit_frames_total %d\n", m.framesTotal.Load())
	fmt.Fprintf(w, "# HELP cockpit_subscribers Streams currently attached.\n")
	fmt.Fprintf(w, "# TYPE cockpit_subscribers gauge\n")
	fmt.Fprintf(w, "cockpit_subscribers %d\n", m.subscribers.Load())
	fmt.Fprintf(w, "# HELP cockpit_subscribers_dropped_total Streams closed for falling behind the broadcast.\n")
	fmt.Fprintf(w, "# TYPE cockpit_subscribers_dropped_total counter\n")
	fmt.Fprintf(w, "cockpit_subscribers_dropped_total %d\n", m.subscribersDropped.Load())
	fmt.Fprintf(w, "# HELP cockpit_samplers_connected Sampler tick streams currently flowing.\n")
	fmt.Fprintf(w, "# TYPE cockpit_samplers_connected gauge\n")
	fmt.Fprintf(w, "cockpit_samplers_connected %d\n", m.samplersConnected.Load())
}
