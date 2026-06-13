// aisucks.app minimal public API. This first v2 slice deliberately exposes
// only health, liveness, the charter page, and a hello endpoint for SDK and
// release-pipeline proof. Product writes return in a later database slice.
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

	"github.com/caddyserver/certmagic"
)

//go:embed web/index.html
var webFS embed.FS

var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "aisucks-api")

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

	metrics := newMetrics()
	app := newServer(page, metrics, domain)

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

	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = os.Getenv("ACME_EMAIL")
	certmagic.DefaultACME.DisableHTTPChallenge = true
	if dir := os.Getenv("CERT_DIR"); dir != "" {
		certmagic.Default.Storage = &certmagic.FileStorage{Path: dir}
	}
	cfg := certmagic.NewDefault()
	if err := cfg.ManageAsync(ctx, []string{domain}); err != nil {
		return fmt.Errorf("certmagic manage %s: %w", domain, err)
	}
	tlsCfg := cfg.TLSConfig()
	tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)
	tlsLn, err := net.Listen("tcp", envOr("LISTEN_TLS", ":8443"))
	if err != nil {
		return err
	}
	https := &http.Server{
		Handler:           app,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          silenced,
	}
	go drainOnShutdown(ctx, https)
	go func() {
		if err := https.ServeTLS(tlsLn, "", ""); err != http.ErrServerClosed {
			logger.Error("tls listener", "error", err)
			os.Exit(1)
		}
	}()

	httpLn, err := net.Listen("tcp", envOr("LISTEN_HTTP", ":8080"))
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           redirectingHTTP(app, domain),
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

func redirectingHTTP(next http.Handler, domain string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/livez":
			next.ServeHTTP(w, r)
			return
		}
		u := *r.URL
		u.Scheme = "https"
		u.Host = domain
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	})
}
