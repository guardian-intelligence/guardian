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

type rollupMetrics struct {
	rowsWritten   atomic.Uint64
	writeFailures atomic.Uint64
	rowsPruned    atomic.Uint64
	reconnects    atomic.Uint64
	lastWriteTsMs atomic.Int64
}

func (m *rollupMetrics) render(w io.Writer) {
	fmt.Fprintf(w, "# HELP cockpit_rollup_rows_written_total Rollup rows persisted.\n")
	fmt.Fprintf(w, "# TYPE cockpit_rollup_rows_written_total counter\n")
	fmt.Fprintf(w, "cockpit_rollup_rows_written_total %d\n", m.rowsWritten.Load())
	fmt.Fprintf(w, "# HELP cockpit_rollup_write_failures_total Frame writes that failed after the stream delivered them.\n")
	fmt.Fprintf(w, "# TYPE cockpit_rollup_write_failures_total counter\n")
	fmt.Fprintf(w, "cockpit_rollup_write_failures_total %d\n", m.writeFailures.Load())
	fmt.Fprintf(w, "# HELP cockpit_rollup_rows_pruned_total Rows deleted past the retention horizon.\n")
	fmt.Fprintf(w, "# TYPE cockpit_rollup_rows_pruned_total counter\n")
	fmt.Fprintf(w, "cockpit_rollup_rows_pruned_total %d\n", m.rowsPruned.Load())
	fmt.Fprintf(w, "# HELP cockpit_rollup_stream_reconnects_total Hub subscription attempts after the first.\n")
	fmt.Fprintf(w, "# TYPE cockpit_rollup_stream_reconnects_total counter\n")
	fmt.Fprintf(w, "cockpit_rollup_stream_reconnects_total %d\n", m.reconnects.Load())
	fmt.Fprintf(w, "# HELP cockpit_rollup_last_write_timestamp_ms Wall time of the last successful write.\n")
	fmt.Fprintf(w, "# TYPE cockpit_rollup_last_write_timestamp_ms gauge\n")
	fmt.Fprintf(w, "cockpit_rollup_last_write_timestamp_ms %d\n", m.lastWriteTsMs.Load())
}

type eventsMetrics struct {
	rowsWritten  atomic.Uint64
	pollFailures atomic.Uint64
	rowsPruned   atomic.Uint64
	lastPollTsMs atomic.Int64
}

func (m *eventsMetrics) render(w io.Writer) {
	fmt.Fprintf(w, "# HELP cockpit_events_rows_written_total Timeline rows persisted.\n")
	fmt.Fprintf(w, "# TYPE cockpit_events_rows_written_total counter\n")
	fmt.Fprintf(w, "cockpit_events_rows_written_total %d\n", m.rowsWritten.Load())
	fmt.Fprintf(w, "# HELP cockpit_events_poll_failures_total Polls that ended in an error.\n")
	fmt.Fprintf(w, "# TYPE cockpit_events_poll_failures_total counter\n")
	fmt.Fprintf(w, "cockpit_events_poll_failures_total %d\n", m.pollFailures.Load())
	fmt.Fprintf(w, "# HELP cockpit_events_rows_pruned_total Rows deleted past the retention horizon.\n")
	fmt.Fprintf(w, "# TYPE cockpit_events_rows_pruned_total counter\n")
	fmt.Fprintf(w, "cockpit_events_rows_pruned_total %d\n", m.rowsPruned.Load())
	fmt.Fprintf(w, "# HELP cockpit_events_last_poll_timestamp_ms Wall time of the last successful poll.\n")
	fmt.Fprintf(w, "# TYPE cockpit_events_last_poll_timestamp_ms gauge\n")
	fmt.Fprintf(w, "cockpit_events_last_poll_timestamp_ms %d\n", m.lastPollTsMs.Load())
}
