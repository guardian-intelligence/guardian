// aisucks.app minimal public API. This v2 slice deliberately exposes only
// health, liveness, and the charter page. Product writes return in a later
// database slice.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "aisucks-api")

const defaultAPIVersion = "0.1.0"

func main() {
	if err := run(); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func drainOnShutdown(ctx context.Context, servers ...*http.Server) {
	<-ctx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shCtx)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	domain := strings.TrimSpace(os.Getenv("DOMAIN"))
	if domain == "" {
		return fmt.Errorf("DOMAIN is required")
	}

	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		return fmt.Errorf("read embedded page: %w", err)
	}

	apiVersion := envOr("APP_VERSION", defaultAPIVersion)
	metrics := newMetrics(apiVersion)
	app := newServer(page, metrics, domain, apiVersion)

	silenced := log.New(io.Discard, "", 0)

	diag := &http.Server{
		Handler:           newDiagServer(metrics),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          silenced,
	}
	diagLn, err := net.Listen("tcp", envOr("DIAG_ADDR", ":9090"))
	if err != nil {
		return err
	}
	go drainOnShutdown(ctx, diag)
	go func() {
		if err := diag.Serve(diagLn); err != http.ErrServerClosed {
			logger.Error("diagnostics listener", "error", err)
			os.Exit(1)
		}
	}()

	httpLn, err := net.Listen("tcp", envOr("LISTEN_HTTP", ":8080"))
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          silenced,
	}
	go drainOnShutdown(ctx, httpServer)
	logger.Info("serving", "domain", domain)
	if err := httpServer.Serve(httpLn); err != http.ErrServerClosed {
		return err
	}
	return nil
}
