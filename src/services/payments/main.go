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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	tb "github.com/tigerbeetle/tigerbeetle-go"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(1)
	}
	rootCtx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()
	traceShutdown, err := initTracing(rootCtx, cfg.OTLPEndpoint)
	if err != nil {
		slog.Error("tracing initialization", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = traceShutdown(ctx)
	}()

	pool, err := pgxpool.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	conn, err := pool.Acquire(rootCtx)
	if err != nil {
		slog.Error("database acquire", "error", err)
		os.Exit(1)
	}
	if err := runMigrations(rootCtx, conn.Conn()); err != nil {
		conn.Release()
		slog.Error("database migration", "error", err)
		os.Exit(1)
	}
	conn.Release()
	queries := paymentdb.New(pool)

	verifier, err := newOIDCVerifier(rootCtx, cfg.OIDCIssuer, cfg.OIDCClientID)
	if err != nil {
		slog.Error("OIDC discovery", "error", err)
		os.Exit(1)
	}
	stripeClient := newStripeClient(cfg.StripeAPIKey, cfg.StripeAPIBase)
	if err := stripeClient.VerifyAccount(rootCtx, cfg.StripeAccountID); err != nil {
		slog.Error("Stripe sandbox account binding", "error", err)
		os.Exit(1)
	}
	tigerBeetle, err := tb.NewClient(cfg.TigerBeetleClusterID, cfg.TigerBeetleAddresses)
	if err != nil {
		slog.Error("TigerBeetle client", "error", err)
		os.Exit(1)
	}
	defer tigerBeetle.Close()
	if err := tigerBeetle.Nop(); err != nil {
		slog.Error("TigerBeetle readiness", "error", err)
		os.Exit(1)
	}
	journal, err := newS3Journal(rootCtx, cfg)
	if err != nil {
		slog.Error("R2 recovery journal", "error", err)
		os.Exit(1)
	}
	metrics := newPaymentMetrics(prometheus.DefaultRegisterer)
	metrics.accountBinding.Set(1)
	go monitorStripeAccountBinding(
		rootCtx,
		stripeClient,
		cfg.StripeAccountID,
		metrics,
		5*time.Minute,
	)
	ledger := &ledgerGateway{
		queries: queries,
		tb:      tigerBeetle,
		journal: journal,
	}
	projector := &providerProjector{
		accountID: cfg.StripeAccountID,
		queries:   queries,
		stripe:    stripeClient,
		ledger:    ledger,
	}
	reconciler := &balanceReconciler{
		accountID: cfg.StripeAccountID,
		queries:   queries,
		stripe:    stripeClient,
		metrics:   metrics,
	}
	go projector.run(rootCtx, cfg.EventWorkerInterval)
	go reconciler.run(rootCtx, cfg.ReconciliationInterval)
	go metrics.refreshLoop(rootCtx, queries, 15*time.Second)

	server := &paymentServer{
		cfg:        cfg,
		queries:    queries,
		stripe:     stripeClient,
		verifier:   verifier,
		authorizer: newAuthorizationChecker(cfg.AuthorizationAPIURL, cfg.AuthorizationCheckToken),
		metrics:    metrics,
		databaseReady: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
		tigerBeetleReady: tigerBeetle.Nop,
	}
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	shutdown := make(chan struct{})
	go func() {
		<-rootCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		close(shutdown)
	}()
	slog.Info(
		"payments listening",
		"address", cfg.Listen,
		"stripe_account_id", cfg.StripeAccountID,
		"ledger", syntheticLedger,
		"customer_checkout_enabled", cfg.CustomerCheckoutEnabled,
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("payments server", "error", err)
		os.Exit(1)
	}
	<-shutdown
}
