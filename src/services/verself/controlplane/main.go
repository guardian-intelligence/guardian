// verself-runner control plane — stage (a): GitHub workflow_job webhook
// ingest with a durable delivery ledger, an async worker + reconciliation
// sweeper (webhook payloads are hints, the GitHub API is truth), provider
// demand/assignment records, and a per-PR comment engine. No runner
// provisioning yet: capacity/JIT are stage (b), cache logic is stage (c).
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracing, err := initTracing(ctx, os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		slog.Error("tracing", "err", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		slog.Error("postgres pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	initCtx, initCancel := context.WithTimeout(ctx, 60*time.Second)
	defer initCancel()
	if err := pool.Ping(initCtx); err != nil {
		slog.Error("postgres ping", "err", err)
		os.Exit(1)
	}
	if err := applyMigrations(initCtx, pool); err != nil {
		slog.Error("migrations", "err", err)
		os.Exit(1)
	}

	st := &pgStore{pool: pool}
	gh, err := newGitHubClient(cfg)
	if err != nil {
		slog.Error("github client", "err", err)
		os.Exit(1)
	}
	tracer := otel.Tracer(serviceName)

	ws := &webhookServer{secret: []byte(cfg.webhookSecret), inbox: st, tracer: tracer, now: time.Now}
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		(&worker{st: st, gh: gh, cfg: cfg, tracer: tracer}).run(ctx)
	}()
	go func() {
		defer loops.Done()
		(&commenter{st: st, gh: gh, cfg: cfg, tracer: tracer}).run(ctx)
	}()

	mux := http.NewServeMux()
	// Method handling is the handler's own (405 + Allow with a problem doc),
	// so no method pattern here.
	mux.HandleFunc("/api/v1/github/webhooks", ws.handleWebhook)
	health := func(w http.ResponseWriter, r *http.Request) {
		hctx, hcancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer hcancel()
		if err := st.Ping(hctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	}
	// Both gates sit behind completed migrations by construction: the server
	// only starts after applyMigrations returned.
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", health)

	httpSrv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("shutting down")
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer scancel()
		_ = httpSrv.Shutdown(sctx)
	}()

	slog.Info("listening", "addr", cfg.listenAddr, "api", cfg.apiBaseURL,
		"runner_class_prefix", cfg.runnerClassPrefix, "worker_interval", cfg.workerInterval.String())
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
	// Drain the worker and commenter before exit: their in-flight tick
	// (running on a non-cancelable work context, see worker.run) finishes its
	// delivery transitions instead of leaving rows in 'processing' for the 2m
	// stale reclaim on every deploy.
	cancel()
	loops.Wait()
	_ = shutdownTracing(context.Background())
}
