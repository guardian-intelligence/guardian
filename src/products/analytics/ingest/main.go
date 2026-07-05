// analytics-ingest — the single server-side door for the analytics/fraud
// event pipeline. Accepts Connect Publish batches at
// /api/events/guardian.analytics.v1.EventService/Publish, derives every
// trust-bearing field server-side (receipt time, verified client IP + tier
// from the edge headers, UA, site from Host, skew), enforces the wire
// contract value-level, and batch-inserts into guardian_analytics.events.
// Design + evidence: docs/analytics-storage-design.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	"github.com/guardian-intelligence/guardian/src/platform/connectpolicy"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	addr := envOr("INGEST_LISTEN", ":8080")
	chAddr := envOr("CLICKHOUSE_ADDR", "clickhouse-analytics.guardian-analytics.svc.cozy.local:9000")
	chUser := envOr("CLICKHOUSE_USER", "ingest")
	chPassword := os.Getenv("CLICKHOUSE_PASSWORD")
	// OTLP gRPC endpoint of the in-namespace collector; unset ⇒ no-op tracer.
	otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")

	tpShutdown, err := initTracing(context.Background(), otelEndpoint)
	if err != nil {
		slog.Warn("tracing init failed; continuing without traces", "err", err)
		tpShutdown = func(context.Context) error { return nil }
	}

	// No WithTrustRemote: /api/events is public and unauthenticated, so a
	// client-supplied traceparent must never become the parent of our server
	// span (it would let anyone mint or graft trace IDs in the owned trace
	// store). The default records the remote context as a link on a fresh
	// root instead. Client trace linkage still flows through events.trace_id.
	interceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		slog.Error("otelconnect init", "err", err)
		os.Exit(1)
	}

	sink, err := newClickHouseSink(chAddr, "guardian_analytics", chUser, chPassword)
	if err != nil {
		slog.Error("clickhouse init", "err", err)
		os.Exit(1)
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := sink.Ping(ctx); err != nil {
			// Start anyway: the batcher retries and the buffer bounds loss;
			// crashing here would couple site availability to ClickHouse.
			slog.Warn("clickhouse not reachable at startup", "err", err)
		}
		cancel()
	}

	batch := newBatcher(sink, 10_000, 10*time.Second, 100_000)
	svc := &eventService{batch: batch, now: time.Now}

	srv := &http.Server{
		Addr: addr,
		// h2c lets the ingress speak HTTP/2 to us if configured; plain
		// HTTP/1.1 works identically for Connect unary.
		// Policy interceptor runs first (fail-closed on unpoliced methods),
		// then OTel. No authenticator yet — Publish declares no permission.
		Handler: h2c.NewHandler(
			newHandler(svc, connect.WithInterceptors(connectpolicy.NewInterceptor(nil), interceptor)),
			&http2.Server{},
		),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	// ListenAndServe returns the moment Shutdown is CALLED, not when the
	// drain finishes — closing the batcher before Shutdown returns would
	// drop rows from still-draining handlers that were already Accepted.
	drained := make(chan struct{})
	go func() {
		<-stop
		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		close(drained)
	}()

	slog.Info("listening", "addr", addr, "clickhouse", chAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
	<-drained
	batch.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = tpShutdown(shutdownCtx)
	cancel()
}
