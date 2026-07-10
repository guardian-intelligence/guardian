package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1/cockpitv1connect"
)

// The rollup writer is the warm tier: it subscribes to the hub's public
// frame stream like any other client, folds each node's second of ticks
// into one min/max/avg row, and keeps exactly the stream's horizon in
// Postgres — Electric hands a joining client the last hour from here while
// the live edge rides the stream.

func runRollup(args []string) error {
	fs := flag.NewFlagSet("rollup", flag.ExitOnError)
	hub := fs.String("hub", "", "cockpit hub base URL or host:port (required)")
	listen := fs.String("listen", ":8080", "healthz/metrics listen address")
	retention := fs.Duration("retention", time.Hour, "row retention horizon")
	_ = fs.Parse(args)
	if *hub == "" {
		return errors.New("--hub is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connection config comes from the libpq PG* environment variables
	// (PGHOST, PGDATABASE, PGUSER, PGPASSWORD), which pgx honors natively.
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return fmt.Errorf("pg config: %w", err)
	}
	defer pool.Close()

	m := &rollupMetrics{}
	w := &rollupWriter{pool: pool, m: m, retention: *retention}
	go w.runSubscriber(ctx, *hub)
	go w.runPruner(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.render(rw)
	})
	slog.Info("rollup listening", "addr", *listen, "hub", *hub, "retention", retention.String())
	return serveUntilSignal(ctx, cancel, *listen, mux)
}

// rollupRow is one node-second of persisted telemetry, mirroring the
// cockpit_metrics table.
type rollupRow struct {
	tsMs      int64
	node      string
	cpuMinBp  uint32
	cpuMaxBp  uint32
	cpuAvgBp  uint32
	memUsedBp uint32
}

// rollupRows folds one decoded frame into per-node rows. Series with no
// name yet are skipped: the hub's first frame on a connection is always a
// keyframe, so this only guards a decoder fed out of order.
func rollupRows(series []nodeSeries) []rollupRow {
	rows := make([]rollupRow, 0, len(series))
	for _, s := range series {
		if s.name == "" || len(s.cpuBp) == 0 {
			continue
		}
		min, max, sum := s.cpuBp[0], s.cpuBp[0], uint64(0)
		for _, v := range s.cpuBp {
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
			sum += uint64(v)
		}
		n := uint64(len(s.cpuBp))
		rows = append(rows, rollupRow{
			tsMs:      s.baseTsMs,
			node:      s.name,
			cpuMinBp:  min,
			cpuMaxBp:  max,
			cpuAvgBp:  uint32((sum + n/2) / n),
			memUsedBp: s.memBp,
		})
	}
	return rows
}

type rollupWriter struct {
	pool      *pgxpool.Pool
	m         *rollupMetrics
	retention time.Duration
}

const insertRollupSQL = `INSERT INTO cockpit_metrics
	(ts, node, cpu_min_bp, cpu_max_bp, cpu_avg_bp, mem_used_bp)
	VALUES (to_timestamp($1::double precision / 1000.0), $2, $3, $4, $5, $6)
	ON CONFLICT (ts, node) DO NOTHING`

// writeRows persists one frame's rows. ON CONFLICT DO NOTHING because every
// reconnect re-bursts the hub's most recent full second.
func (w *rollupWriter) writeRows(ctx context.Context, rows []rollupRow) error {
	if len(rows) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		b.Queue(insertRollupSQL, r.tsMs, r.node, r.cpuMinBp, r.cpuMaxBp, r.cpuAvgBp, r.memUsedBp)
	}
	if err := w.pool.SendBatch(ctx, b).Close(); err != nil {
		return err
	}
	w.m.rowsWritten.Add(uint64(len(rows)))
	w.m.lastWriteTsMs.Store(time.Now().UnixMilli())
	return nil
}

// runSubscriber keeps one subscription to the hub flowing, reconnecting
// with a flat backoff. Connection-state logging is edge-triggered — a dark
// hub retries every second for hours, and one warn per attempt is noise.
func (w *rollupWriter) runSubscriber(ctx context.Context, hub string) {
	client := cockpitv1connect.NewCockpitStreamServiceClient(
		&http.Client{Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}},
		samplerBaseURL(hub),
	)
	degraded := false
	for ctx.Err() == nil {
		w.consumeFrames(ctx, client, hub, &degraded)
		w.m.reconnects.Add(1)
		sleepCtx(ctx, time.Second)
	}
}

func (w *rollupWriter) consumeFrames(ctx context.Context, client cockpitv1connect.CockpitStreamServiceClient, hub string, degraded *bool) {
	stream, err := client.Subscribe(ctx, connect.NewRequest(&cockpitv1.SubscribeRequest{}))
	if err != nil {
		if !*degraded && ctx.Err() == nil {
			slog.Warn("hub unreachable; retrying every second", "hub", hub, "err", err)
			*degraded = true
		}
		return
	}
	defer func() { _ = stream.Close() }()

	dec := newFrameDecoder()
	for stream.Receive() {
		if *degraded {
			slog.Info("hub stream recovered", "hub", hub)
			*degraded = false
		}
		series, err := dec.decode(stream.Msg())
		if err != nil {
			slog.Warn("frame decode failed; resubscribing", "hub", hub, "err", err)
			return
		}
		if err := w.writeRows(ctx, rollupRows(series)); err != nil && ctx.Err() == nil {
			w.m.writeFailures.Add(1)
			slog.Warn("rollup write failed", "err", err)
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil && !*degraded {
		slog.Warn("hub stream ended", "hub", hub, "err", err)
		*degraded = true
	}
}

// runPruner deletes rows past the retention horizon once a minute, keeping
// the table at ~nodes×3600 rows.
func (w *rollupWriter) runPruner(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tag, err := w.pool.Exec(ctx,
				`DELETE FROM cockpit_metrics WHERE ts < now() - make_interval(secs => $1)`,
				w.retention.Seconds())
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("prune failed", "err", err)
				}
				continue
			}
			w.m.rowsPruned.Add(uint64(tag.RowsAffected()))
		}
	}
}
