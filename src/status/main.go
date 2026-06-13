// status.guardianintelligence.org: the public fleet status page. The page IS
// a TOML document — one isDeploying boolean per workload on this site, grouped
// by namespace. A goroutine re-queries the site-local VictoriaMetrics and
// atomically swaps a rendered immutable snapshot; handlers only ever serve
// cached bytes (zero per-request queries). Same document in three encodings
// (/status.toml /status.json /status.yaml) plus a script-free HTML wrapper
// at /.
//
// One page per site, this site only (cross-site isolation): the page never
// reaches into a sibling's control plane. When VictoriaMetrics returns no
// workload metrics the page says "unknown" with a reason — never a green
// light invented from missing data.
package main

import (
	"context"
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

// logger is the process-wide structured logger: JSON to stderr,
// process-lifecycle events only (no request-scoped data — same posture as
// aisucks even though this page has no users to protect; no access logs).
var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "status")

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

// drainOnShutdown finishes in-flight requests when the pod is terminated,
// bounded under the pod's terminationGracePeriodSeconds.
func drainOnShutdown(ctx context.Context, servers ...*http.Server) {
	<-ctx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(shCtx)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// The manifest passes the cluster name; accept both spellings so the
	// template stays a dumb value pass-through.
	site := strings.TrimPrefix(os.Getenv("SITE"), "guardian-")
	switch site {
	case "dev", "gamma", "prod":
	default:
		return fmt.Errorf("SITE must be dev, gamma or prod (optionally guardian- prefixed), got %q", os.Getenv("SITE"))
	}
	vmURL := os.Getenv("VM_URL")
	if vmURL == "" {
		return fmt.Errorf("VM_URL is required")
	}

	srv := newServer()
	col := newCollector(vmURL, site)
	go col.loop(ctx, srv.publish)

	// The stdlib's default Server.ErrorLog writes "TLS handshake error from
	// <client-ip>" lines to stderr; keep client addresses out of any log
	// stream (the aisucks charter posture, applied fleet-wide).
	silenced := log.New(io.Discard, "", 0)

	// TLS on LISTEN_TLS when DOMAINS is set. The pod sits behind the Cilium
	// Gateway's TLS passthrough (SNI routing, docs/architecture/gateway.md):
	// no :80 reaches this pod, so the HTTP-01 challenge is disabled and
	// issuance rides TLS-ALPN-01 through the passthrough natively.
	// ManageAsync, never ManageSync: a status hostname may not resolve yet
	// when the site converges — the pod must serve HTTP and /healthz
	// immediately, and TLS comes up on its own once DNS lands.
	if domainsEnv := strings.TrimSpace(os.Getenv("DOMAINS")); domainsEnv != "" {
		var domains []string
		for _, d := range strings.Split(domainsEnv, ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
		certmagic.DefaultACME.Agreed = true
		certmagic.DefaultACME.Email = os.Getenv("ACME_EMAIL")
		certmagic.DefaultACME.DisableHTTPChallenge = true
		if dir := os.Getenv("CERT_DIR"); dir != "" {
			certmagic.Default.Storage = &certmagic.FileStorage{Path: dir}
		}
		cfg := certmagic.NewDefault()
		if err := cfg.ManageAsync(ctx, domains); err != nil {
			return fmt.Errorf("certmagic manage %v: %w", domains, err)
		}
		tlsCfg := cfg.TLSConfig()
		tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)
		lnTLS, err := net.Listen("tcp", envOr("LISTEN_TLS", ":8443"))
		if err != nil {
			return err
		}
		hsTLS := &http.Server{
			Handler:           srv,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          silenced,
		}
		go drainOnShutdown(ctx, hsTLS)
		go func() {
			// The TLS listener is the public face once DNS exists; dying
			// loudly beats a pod that looks Ready but serves nothing.
			if err := hsTLS.ServeTLS(lnTLS, "", ""); err != http.ErrServerClosed {
				logger.Error("tls listener", "error", err)
				os.Exit(1)
			}
		}()
		logger.Info("serving TLS", "domains", domains)
	}

	// Plain HTTP always: kubelet's readiness probe and in-cluster consumers
	// hit this regardless of certificate state.
	addr := envOr("LISTEN_HTTP", ":8080")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second, ErrorLog: silenced}
	go drainOnShutdown(ctx, hs)
	logger.Info("serving HTTP", "addr", addr, "site", site)
	if err := hs.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}
